package mail

import (
	"fmt"
	"time"

	"git.mehdix.org/alborz/internal/kv"
)

const (
	sendClaimOwner  = "alborz.mail.send"
	sendClaimFormat = 1
	sendClaiming    = byte(1)
	sendAccepted    = byte(2)
	sendComplete    = byte(3)
	sendPartial     = byte(4)
	// A short window covers immediate browser and network retries after SMTP accepts DATA.
	sendClaimKeep = 10 * time.Minute
)

// claimSend keeps one submission's state across retries and restarts.
func (p *Mailboxes) claimSend(account, messageID string) (state byte, fresh bool, err error) {
	db, err := p.claimsDB()
	if err != nil {
		return 0, false, err
	}
	at := sendClaimOwner + "/" + p.server.LoginKey.Names().Of(account+"\x00"+messageID)
	key := p.server.LoginKey.Key(sendClaimOwner)
	now := time.Now()
	p.sendMu.Lock()
	defer p.sendMu.Unlock()
	if p.sendActive == nil {
		p.sendActive = make(map[string]bool)
	}
	err = db.Update(func(tx kv.Tx) error {
		if err := kv.DeleteExpired(tx, sendClaimOwner+"/", now, func(k string, sealed []byte) (time.Time, bool) {
			if p.sendActive[k] {
				return time.Time{}, false
			}
			_, at, err := readSendClaim(key.Open, sealed)
			return at.Add(sendClaimKeep), err == nil
		}); err != nil {
			return err
		}
		if old := tx.Get(at); len(old) != 0 {
			state, _, err = readSendClaim(key.Open, old)
			return err
		}
		state = sendClaiming
		fresh = true
		return putSendClaim(tx, at, key.Seal, state, now)
	})
	if err == nil && fresh {
		p.sendActive[at] = true
	}
	return state, fresh, err
}

func (p *Mailboxes) finishSend(account, messageID string, state byte) error {
	db, err := p.claimsDB()
	if err != nil {
		return err
	}
	at := sendClaimOwner + "/" + p.server.LoginKey.Names().Of(account+"\x00"+messageID)
	key := p.server.LoginKey.Key(sendClaimOwner)
	return db.Update(func(tx kv.Tx) error {
		if state != 0 {
			return putSendClaim(tx, at, key.Seal, state, time.Now())
		}
		return tx.Delete(at)
	})
}

func (p *Mailboxes) endSend(account, messageID string) {
	at := sendClaimOwner + "/" + p.server.LoginKey.Names().Of(account+"\x00"+messageID)
	p.sendMu.Lock()
	delete(p.sendActive, at)
	p.sendMu.Unlock()
}

func putSendClaim(tx kv.Tx, at string, seal func([]byte) []byte, state byte, now time.Time) error {
	stamp, err := now.MarshalBinary()
	if err != nil {
		return err
	}
	return tx.Put(at, seal(append([]byte{state}, stamp...)))
}

func readSendClaim(open func([]byte) ([]byte, error), sealed []byte) (byte, time.Time, error) {
	raw, err := open(sealed)
	if err != nil {
		return 0, time.Time{}, err
	}
	if len(raw) < 2 {
		return 0, time.Time{}, fmt.Errorf("invalid send claim")
	}
	var at time.Time
	if err := at.UnmarshalBinary(raw[1:]); err != nil {
		return 0, time.Time{}, err
	}
	return raw[0], at, nil
}

func (p *Mailboxes) claimsDB() (kv.DB, error) {
	p.sendStoreOnce.Do(func() {
		p.sendStore, p.sendStoreErr = p.server.Data(sendClaimOwner, sendClaimFormat)
	})
	return p.sendStore, p.sendStoreErr
}
