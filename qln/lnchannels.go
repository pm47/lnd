package qln

import (
	"bytes"
	"fmt"

	"github.com/lightningnetwork/lnd/elkrem"
	"github.com/lightningnetwork/lnd/lnutil"
	"github.com/lightningnetwork/lnd/portxo"

	"github.com/btcsuite/fastsha256"
	"github.com/roasbeef/btcd/wire"
	"github.com/roasbeef/btcutil"
)

// Uhh, quick channel.  For now.  Once you get greater spire it upgrades to
// a full channel that can do everything.
type Qchan struct {
	// S for stored (on disk), D for derived

	portxo.PorTxo            // S underlying utxo data
	CloseData     QCloseData // S closing outpoint

	MyPub    [33]byte // D my channel specific pubkey
	TheirPub [33]byte // S their channel specific pubkey

	PeerId [33]byte // D useful for quick traverse of db

	// Refunds are also elkremified
	MyRefundPub    [33]byte // D my refund pubkey for channel break
	TheirRefundPub [33]byte // S their pubkey for channel break

	MyHAKDBase    [33]byte // D my base point for HAKD and timeout keys
	TheirHAKDBase [33]byte // S their base point for HAKD and timeout keys

	// Elkrem is used for revoking state commitments
	ElkSnd *elkrem.ElkremSender   // D derived from channel specific key
	ElkRcv *elkrem.ElkremReceiver // S stored in db

	TimeOut uint16 // blocks for timeout (default 5 for testing)

	State *StatCom // S state of channel
}

// StatComs are State Commitments.
// all elements are saved to the db.
type StatCom struct {
	StateIdx uint64 // this is the n'th state commitment

	MyAmt int64 // my channel allocation
	// their Amt is the utxo.Value minus this
	Delta int32 // fun amount in-transit; is negative for the pusher

	// Elkrem point from counterparty, used to make
	// Homomorphic Adversarial Key Derivation public keys (HAKD)
	ElkPointR     [33]byte // saved to disk, revealable point
	PrevElkPointR [33]byte // When you haven't gotten their revocation elkrem yet.

	ElkPointT     [33]byte // their timeout elk point; needed for script
	PrevElkPointT [33]byte // When you haven't gotten their revocation elkrem yet.

	sig [64]byte // Counterparty's signature (for StatCom tx)
	// don't write to sig directly; only overwrite via fn() call

	// note sig can be nil during channel creation. if stateIdx isn't 0,
	// sig should have a sig.
	// only one sig is ever stored, to prevent broadcasting the wrong tx.
	// could add a mutex here... maybe will later.
}

// QCloseData is the output resulting from an un-cooperative close
// of the channel.  This happens when either party breaks non-cooperatively.
// It describes "your" output, either pkh or time-delay script.
// If you have pkh but can grab the other output, "grabbable" is set to true.
// This can be serialized in a separate bucket

type QCloseData struct {
	// 3 txid / height pairs are stored.  All 3 only are used in the
	// case where you grab their invalid close.
	CloseTxid   wire.ShaHash
	CloseHeight int32
	Closed      bool // if channel is closed; if CloseTxid != -1
}

// GetCloseTxos takes in a tx and sets the QcloseTXO feilds based on the tx.
// It also returns the spendable (u)txos generated by the close.
func (q *Qchan) GetCloseTxos(tx *wire.MsgTx) ([]portxo.PorTxo, error) {
	if tx == nil {
		return nil, fmt.Errorf("IngesGetCloseTxostCloseTx: nil tx")
	}
	txid := tx.TxSha()
	// double check -- does this tx actually close the channel?
	if !(len(tx.TxIn) == 1 && lnutil.OutPointsEqual(tx.TxIn[0].PreviousOutPoint, q.Op)) {
		return nil, fmt.Errorf("tx %s doesn't spend channel outpoint %s",
			txid.String(), q.Op.String())
	}
	// hardcode here now... need to save to qchan struct I guess
	q.TimeOut = 5
	x := q.GetElkZeroOffset()
	if x >= 1<<48 {
		return nil, fmt.Errorf("GetCloseTxos elkrem error, x= %x", x)
	}
	// first, check if cooperative
	txIdx := GetStateIdxFromTx(tx, x)
	if txIdx > q.State.StateIdx { // future state, uhoh.  Crash for now.
		return nil, fmt.Errorf("indicated state %d but we know up to %d",
			txIdx, q.State.StateIdx)
	}

	if txIdx == 0 || len(tx.TxOut) != 2 {
		// must have been cooperative, or something else we don't recognize
		// if simple close, still have a PKH output, find it.
		// so far, assume 1 txo

		// no txindx hint, so it's probably cooperative, so most recent
		elk, err := q.ElkSnd.AtIndex(q.State.StateIdx)
		if err != nil {
			return nil, err
		}
		// hash elkrem into elkrem R scalar (0x72 == 'r')
		theirElkHashR := wire.DoubleSha256SH(append(elk.Bytes(), 0x72))
		theirElkPointR := lnutil.PubFromHash(theirElkHashR)

		myRefundArr := lnutil.AddPubs(theirElkPointR, q.MyRefundPub)
		myPKH := btcutil.Hash160(myRefundArr[:])

		for i, out := range tx.TxOut {
			if len(out.PkScript) < 22 {
				continue // skip to prevent crash
			}
			if bytes.Equal(out.PkScript[2:22], myPKH) { // detected my refund
				var pkhTxo portxo.PorTxo

				pkhTxo.Op.Hash = txid
				pkhTxo.Op.Index = uint32(i)
				pkhTxo.Height = q.CloseData.CloseHeight

				pkhTxo.KeyGen = q.KeyGen

				pkhTxo.PrivKey = theirElkHashR

				// keypath is the same other than use
				pkhTxo.KeyGen.Step[2] = UseChannelRefund

				pkhTxo.Value = tx.TxOut[i].Value
				pkhTxo.Mode = portxo.TxoP2WPKHComp // witness, normal PKH
				pkhTxo.PkScript = tx.TxOut[i].PkScript

				return []portxo.PorTxo{pkhTxo}, nil
			}
		}
		// couldn't find anything... shouldn't happen
		return nil, fmt.Errorf("channel closed but we got nothing!")
	}

	// non-cooperative / break.

	var shIdx, pkhIdx uint32
	cTxos := make([]portxo.PorTxo, 1)
	// sort outputs into PKH and SH
	if len(tx.TxOut[0].PkScript) == 34 {
		shIdx = 0
		pkhIdx = 1
	} else {
		pkhIdx = 0
		shIdx = 1
	}
	// make sure SH output is actually SH
	if len(tx.TxOut[shIdx].PkScript) != 34 {
		return nil, fmt.Errorf("non-p2sh output is length %d, expect 34",
			len(tx.TxOut[shIdx].PkScript))
	}
	// make sure PKH output is actually PKH
	if len(tx.TxOut[pkhIdx].PkScript) != 22 {
		return nil, fmt.Errorf("non-p2wsh output is length %d, expect 22",
			len(tx.TxOut[pkhIdx].PkScript))
	}

	// use the indicated state to generate refund pkh (it may be old)

	// refund PKHs come from the refund base plus their elkrem point R.
	theirElkPointR, err := q.ElkPoint(false, false, txIdx)
	if err != nil {
		return nil, err
	}
	theirElkPointT, err := q.ElkPoint(false, true, txIdx)
	if err != nil {
		return nil, err
	}

	myRefundArr := lnutil.AddPubs(theirElkPointR, q.MyRefundPub)
	myPKH := btcutil.Hash160(myRefundArr[:])

	// indirectly check if SH is mine
	if !bytes.Equal(tx.TxOut[pkhIdx].PkScript[2:22], myPKH) {
		// ------------pkh not mine; assume SH is mine
		// build script to store in porTxo
		timeoutPub := lnutil.AddPubs(q.MyHAKDBase, theirElkPointT)
		revokePub := lnutil.AddPubs(q.TheirHAKDBase, theirElkPointR)

		script, err := CommitScript2(revokePub, timeoutPub, q.TimeOut)
		if err != nil {
			return nil, err
		}

		var shTxo portxo.PorTxo // create new utxo and copy into it
		// use txidx's elkrem as it may not be most recent
		elk, err := q.ElkSnd.AtIndex(txIdx)
		if err != nil {
			return nil, err
		}
		// keypath is the same, except for use
		shTxo.KeyGen = q.KeyGen

		shTxo.Op.Hash = txid
		shTxo.Op.Index = shIdx
		shTxo.Height = q.CloseData.CloseHeight
		shTxo.KeyGen.Step[2] = UseChannelHAKDBase
		// hash elkrem into elkrem T scalar (0x74 == 't')
		shTxo.PrivKey = wire.DoubleSha256SH(append(elk.Bytes(), 0x74))

		shTxo.Mode = portxo.TxoP2WSHComp

		shTxo.Value = tx.TxOut[shIdx].Value
		shTxo.Seq = uint32(q.TimeOut)

		// script check
		genSH := fastsha256.Sum256(script)
		if !bytes.Equal(genSH[:], tx.TxOut[shIdx].PkScript[2:34]) {
			fmt.Printf("got different observed and generated SH scripts.\n")
			fmt.Printf("in %s:%d, see %x\n", txid, shIdx, tx.TxOut[shIdx].PkScript)
			fmt.Printf("generated %x \n", genSH)
			fmt.Printf("revokable pub %x\ntimeout pub %x\n", revokePub, timeoutPub)
		}
		shTxo.PkScript = script

		cTxos[0] = shTxo
		// if SH is mine we're done
		return cTxos, nil
	}

	// ---------- pkh is mine
	var pkhTxo portxo.PorTxo // create new utxo and copy into it

	// use txidx's elkrem as it may not be most recent
	elk, err := q.ElkSnd.AtIndex(txIdx)
	if err != nil {
		return nil, err
	}

	elkHashR := wire.DoubleSha256SH(append(elk.Bytes(), 0x72)) // 'r'
	elkPointR := lnutil.PubFromHash(elkHashR)
	combined := lnutil.AddPubs(elkPointR, q.MyRefundPub)
	pkh := btcutil.Hash160(combined[:])
	// check if re-created script matches observed script (hash)
	if !bytes.Equal(tx.TxOut[pkhIdx].PkScript[2:], pkh) {
		// probably should error out here
		fmt.Printf("got different observed and generated pkh scripts.\n")
		fmt.Printf("in %s : %d see %x\n", txid, pkhIdx, tx.TxOut[pkhIdx].PkScript)
		fmt.Printf("generated %x from sender (/ their) elkR %d\n", pkh, txIdx)
		fmt.Printf("base refund pub %x\n", q.MyRefundPub)
	}

	pkhTxo.Op.Hash = txid
	pkhTxo.Op.Index = pkhIdx
	pkhTxo.Height = q.CloseData.CloseHeight
	// keypath same, use different
	pkhTxo.KeyGen = q.KeyGen
	// same keygen as underlying channel, but use is refund
	pkhTxo.KeyGen.Step[2] = UseChannelRefund
	// hash elkrem into elkrem R scalar (0x72 == 'r')
	pkhTxo.PrivKey = elkHashR
	pkhTxo.Mode = portxo.TxoP2WPKHComp
	pkhTxo.Value = tx.TxOut[pkhIdx].Value
	// PKH, so script is easy
	pkhTxo.PkScript = tx.TxOut[pkhIdx].PkScript
	cTxos[0] = pkhTxo

	// OK, it's my PKH, but can I grab the SH???
	if txIdx < q.State.StateIdx {
		// invalid previous state, can be grabbed!
		// make MY elk points
		myElkPointR, err := q.ElkPoint(true, false, txIdx)
		if err != nil {
			return nil, err
		}
		myElkPointT, err := q.ElkPoint(true, true, txIdx)
		if err != nil {
			return nil, err
		}
		timeoutPub := lnutil.AddPubs(q.TheirHAKDBase, myElkPointT)
		revokePub := lnutil.AddPubs(q.MyHAKDBase, myElkPointR)
		script, err := CommitScript2(revokePub, timeoutPub, q.TimeOut)
		if err != nil {
			return nil, err
		}
		// script check
		genSH := fastsha256.Sum256(script)
		if !bytes.Equal(genSH[:], tx.TxOut[shIdx].PkScript[2:34]) {
			fmt.Printf("got different observed and generated SH scripts.\n")
			fmt.Printf("in %s:%d, see %x\n", txid, shIdx, tx.TxOut[shIdx].PkScript)
			fmt.Printf("generated %x \n", genSH)
			fmt.Printf("revokable pub %x\ntimeout pub %x\n", revokePub, timeoutPub)
		}

		// myElkHashR added to HAKD private key
		elk, err := q.ElkRcv.AtIndex(txIdx)
		if err != nil {
			return nil, err
		}

		var shTxo portxo.PorTxo // create new utxo and copy into it
		shTxo.KeyGen = q.KeyGen
		shTxo.Op.Hash = txid
		shTxo.Op.Index = shIdx
		shTxo.Height = q.CloseData.CloseHeight

		shTxo.KeyGen.Step[2] = UseChannelHAKDBase

		shTxo.PrivKey = wire.DoubleSha256SH(append(elk.Bytes(), 0x72)) // 'r'

		shTxo.PkScript = script

		shTxo.Value = tx.TxOut[shIdx].Value
		shTxo.Mode = portxo.TxoP2WSHComp
		shTxo.Seq = 1 // 1 means grab immediately
		cTxos = append(cTxos, shTxo)
	}

	return cTxos, nil
}

// ChannelInfo prints info about a channel.
func (nd *LnNode) QchanInfo(q *Qchan) error {
	// display txid instead of outpoint because easier to copy/paste
	fmt.Printf("CHANNEL %s h:%d %s cap: %d\n",
		q.Op.Hash.String(), q.Height, q.KeyGen.String(), q.Value)
	fmt.Printf("\tPUB mine:%x them:%x REFBASE mine:%x them:%x BASE mine:%x them:%x\n",
		q.MyPub[:4], q.TheirPub[:4], q.MyRefundPub[:4], q.TheirRefundPub[:4],
		q.MyHAKDBase[:4], q.TheirHAKDBase[:4])
	if q.State == nil || q.ElkRcv == nil {
		fmt.Printf("\t no valid state or elkrem\n")
	} else {

		fmt.Printf("\ta %d (them %d) state index %d\n",
			q.State.MyAmt, q.Value-q.State.MyAmt, q.State.StateIdx)

		fmt.Printf("\tdelta:%d HAKD:%x prevHAKD:%x elk@ %d\n",
			q.State.Delta, q.State.ElkPointR[:4], q.State.PrevElkPointR[:4],
			q.ElkRcv.UpTo())
		elkp, _ := q.ElkPoint(false, false, q.State.StateIdx)
		myRefPub := lnutil.AddPubs(q.MyRefundPub, elkp)

		theirRefPub := lnutil.AddPubs(q.TheirRefundPub, q.State.ElkPointT)
		fmt.Printf("\tMy Refund: %x Their Refund %x\n", myRefPub[:4], theirRefPub[:4])
	}

	if !q.CloseData.Closed { // still open, finish here
		return nil
	}

	fmt.Printf("\tCLOSED at height %d by tx: %s\n",
		q.CloseData.CloseHeight, q.CloseData.CloseTxid.String())
	//	clTx, err := t.GetTx(&q.CloseData.CloseTxid)
	//	if err != nil {
	//		return err
	//	}
	//	ctxos, err := q.GetCloseTxos(clTx)
	//	if err != nil {
	//		return err
	//	}

	//	if len(ctxos) == 0 {
	//		fmt.Printf("\tcooperative close.\n")
	//		return nil
	//	}

	//	fmt.Printf("\tClose resulted in %d spendable txos\n", len(ctxos))
	//	if len(ctxos) == 2 {
	//		fmt.Printf("\t\tINVALID CLOSE!!!11\n")
	//	}
	//	for i, u := range ctxos {
	//		fmt.Printf("\t\t%d) amt: %d spendable: %d\n", i, u.Value, u.Seq)
	//	}
	return nil
}

// GetElkZeroOffset returns a 48-bit uint (cast up to 8 bytes) based on the sender
// and receiver elkrem at index 0.  If there's an error, it returns ff...
func (q *Qchan) GetElkZeroOffset() uint64 {
	theirZero, err := q.ElkRcv.AtIndex(0)
	if err != nil {
		fmt.Printf(err.Error())
		return 0xffffffffffffffff
	}
	myZero, err := q.ElkSnd.AtIndex(0)
	if err != nil {
		fmt.Printf(err.Error())
		return 0xffffffffffffffff
	}
	theirBytes := theirZero.Bytes()
	myBytes := myZero.Bytes()
	x := make([]byte, 8)
	for i := 2; i < 8; i++ {
		x[i] = myBytes[i] ^ theirBytes[i]
	}

	// only 48 bits so will be OK when cast to signed 64 bit
	return uint64(lnutil.BtI64(x[:]))
}

// MakeTheirCurElkPoint makes the current state elkrem points to send out
func (q *Qchan) MakeTheirCurElkPoints() (r, t [33]byte, err error) {
	// generate revocable elkrem point
	r, err = q.ElkPoint(false, false, q.State.StateIdx)
	if err != nil {
		return
	}
	// generate timeout elkrem point
	t, err = q.ElkPoint(false, true, q.State.StateIdx)
	return
}

// ElkPoint generates an elkrem Point.  "My" elkrem point is the point
// I receive from the counter party, and can create after the state has
// been revoked.  "Their" elkrem point (mine=false) is generated from my elkrem
// sender at any index.
// Elkrem points are sub-hashes of the hash coming from the elkrem tree.
// There are "time" and "revoke" elkrem points, which are just sha2d(elk, "t")
// and sha2d(elk, "r") of the hash from the elkrem tree.
// Having different points prevents observers from distinguishing the channel
// when they have the HAKD base points but not the elkrem point.
func (q *Qchan) ElkPoint(mine, time bool, idx uint64) (p [33]byte, err error) {
	// sanity check
	if q == nil || q.ElkSnd == nil || q.ElkRcv == nil { // can't do anything
		err = fmt.Errorf("can't access elkrem")
		return
	}
	elk := new(wire.ShaHash)

	if mine { // make mine based on receiver
		elk, err = q.ElkRcv.AtIndex(idx)
	} else { // make theirs based on sender
		elk, err = q.ElkSnd.AtIndex(idx)
	}
	// elkrem problem, error out here
	if err != nil {
		return
	}

	if time {
		*elk = wire.DoubleSha256SH(append(elk.Bytes(), 0x74)) // ascii "t"
	} else {
		*elk = wire.DoubleSha256SH(append(elk.Bytes(), 0x72)) // ascii "r"
	}

	// turn the hash into a point
	p = lnutil.PubFromHash(*elk)
	return
}

// IngestElkrem takes in an elkrem hash, performing 2 checks:
// that it produces the proper elk point, and that it fits into the elkrem tree.
// if both of these are the case it updates the channel state, removing the
// revoked point. If either of these checks fail, and definitely the second one
// fails, I'm pretty sure the channel is not recoverable and needs to be closed.
func (q *Qchan) IngestElkrem(elk *wire.ShaHash) error {
	if elk == nil {
		return fmt.Errorf("IngestElkrem: nil hash")
	}

	// first verify the elkrem insertion (this only performs checks 1/2 the time, so
	// 1/2 the time it'll work even if the elkrem is invalid, oh well)
	err := q.ElkRcv.AddNext(elk)
	if err != nil {
		return err
	}
	fmt.Printf("ingested hash, receiver now has up to %d\n", q.ElkRcv.UpTo())

	// if this is state 1, then we have elkrem 0 and we can stop here.
	// there's nothing to revoke.
	if q.State.StateIdx == 1 {
		return nil
	}

	// next verify if the elkrem produces the previous elk point.
	// We don't actually use the private key operation here, because we can
	// do the same operation on our pubkey that they did, and we have faith
	// in the mysterious power of abelian group homomorphisms that the private
	// key modification will also work.

	// Make r and t points from received elk hash
	CheckR := lnutil.PubFromHash(wire.DoubleSha256SH(append(elk.Bytes(), 0x72))) // r
	CheckT := lnutil.PubFromHash(wire.DoubleSha256SH(append(elk.Bytes(), 0x74))) // t

	// see if it matches previous elk point
	if CheckR != q.State.PrevElkPointR || CheckT != q.State.PrevElkPointT {
		// didn't match, the whole channel is borked.
		return fmt.Errorf("hash %x (index %d) fits tree but creates wrong elkpoint!",
			elk[:8], q.State.PrevElkPointR, q.State.PrevElkPointT)
	}

	// it did match, so we can clear the previous HAKD pub
	var empty [33]byte
	q.State.PrevElkPointR = empty
	q.State.PrevElkPointT = empty

	return nil
}
