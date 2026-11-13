package passkeytest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

// Authenticator is a software authenticator and the browser around it: it answers the options a
// server gave to navigator.credentials.create and .get with the JSON a browser would send. Its fields
// are the knobs a test turns to break a ceremony.
type Authenticator struct {
	key      *ecdsa.PrivateKey
	CredID   []byte
	userID   []byte
	AAGUID   [16]byte
	Counts   bool   // whether the sign counter counts (many passkeys keep it at 0)
	Counter  uint32 // the next value written when Counts
	Origin   string // the origin the "browser" reports (default: the first of the options' rp id as https)
	RPID     string // the id the authenticator hashes; empty means the options'
	UV       bool   // user verification performed (default true)
	BE, BS   bool   // backup eligible / backup state
	WrongKey bool   // sign with another key
	// IgnoreAllowList answers a request whose allowed credentials do not include this one (a browser would not).
	IgnoreAllowList bool
}

// New returns an authenticator that verifies the user, counts, and reports origin.
func New(origin string) *Authenticator {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	id := make([]byte, 32)
	_, _ = rand.Read(id)
	return &Authenticator{key: key, CredID: id, Counts: true, Counter: 1, Origin: origin, UV: true}
}

var b64 = base64.RawURLEncoding

type creationOptions struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
		RP        struct {
			ID string `json:"id"`
		} `json:"rp"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		AuthenticatorSelection struct {
			ResidentKey      string `json:"residentKey"`
			UserVerification string `json:"userVerification"`
		} `json:"authenticatorSelection"`
		Exclude []struct {
			ID string `json:"id"`
		} `json:"excludeCredentials"`
	} `json:"publicKey"`
}

type requestOptions struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
		RPID      string `json:"rpId"`
		Allow     []struct {
			ID string `json:"id"`
		} `json:"allowCredentials"`
		UserVerification string `json:"userVerification"`
	} `json:"publicKey"`
}

func (a *Authenticator) flags(attested bool) byte {
	f := byte(0x01)
	if a.UV {
		f |= 0x04
	}
	if a.BE {
		f |= 0x08
	}
	if a.BS {
		f |= 0x10
	}
	if attested {
		f |= 0x40
	}
	return f
}

func (a *Authenticator) count() uint32 {
	if !a.Counts {
		return 0
	}
	c := a.Counter
	a.Counter++
	return c
}

func (a *Authenticator) rpHash(rp string) [32]byte {
	if a.RPID != "" {
		rp = a.RPID
	}
	return sha256.Sum256([]byte(rp))
}

func (a *Authenticator) cose() []byte {
	pub := a.key.PublicKey
	x, y := pub.X.FillBytes(make([]byte, 32)), pub.Y.FillBytes(make([]byte, 32))
	enc, err := cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		panic(err)
	}
	out, err := enc.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
	if err != nil {
		panic(err)
	}
	return out
}

// Register answers the options of navigator.credentials.create.
func (a *Authenticator) Register(t testing.TB, options []byte) []byte {
	t.Helper()
	var o creationOptions
	if err := json.Unmarshal(options, &o); err != nil {
		t.Fatal(err)
	}
	if o.PublicKey.AuthenticatorSelection.ResidentKey != "required" || o.PublicKey.AuthenticatorSelection.UserVerification != "required" {
		t.Fatalf("the options do not ask for a resident key and user verification: %+v", o.PublicKey.AuthenticatorSelection)
	}
	for _, e := range o.PublicKey.Exclude {
		if e.ID == b64.EncodeToString(a.CredID) {
			t.Fatal("the authenticator holds an excluded credential")
		}
	}
	a.userID, _ = b64.DecodeString(o.PublicKey.User.ID)
	rp := a.rpHash(o.PublicKey.RP.ID)
	authData := append([]byte{}, rp[:]...)
	authData = append(authData, a.flags(true))
	authData = binary.BigEndian.AppendUint32(authData, a.count())
	authData = append(authData, a.AAGUID[:]...)
	authData = binary.BigEndian.AppendUint16(authData, uint16(len(a.CredID)))
	authData = append(authData, a.CredID...)
	authData = append(authData, a.cose()...)
	att, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": authData})
	if err != nil {
		t.Fatal(err)
	}
	client := a.clientData("webauthn.create", o.PublicKey.Challenge)
	return mustJSON(t, map[string]any{
		"id": b64.EncodeToString(a.CredID), "rawId": b64.EncodeToString(a.CredID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON": b64.EncodeToString(client), "attestationObject": b64.EncodeToString(att),
			"transports": []string{"internal"},
		},
	})
}

// Assert answers the options of navigator.credentials.get.
func (a *Authenticator) Assert(t testing.TB, options []byte) []byte {
	t.Helper()
	var o requestOptions
	if err := json.Unmarshal(options, &o); err != nil {
		t.Fatal(err)
	}
	if len(o.PublicKey.Allow) > 0 && !a.IgnoreAllowList {
		found := false
		for _, e := range o.PublicKey.Allow {
			found = found || e.ID == b64.EncodeToString(a.CredID)
		}
		if !found {
			t.Fatal("the authenticator holds none of the allowed credentials")
		}
	}
	rp := a.rpHash(o.PublicKey.RPID)
	authData := append([]byte{}, rp[:]...)
	authData = append(authData, a.flags(false))
	authData = binary.BigEndian.AppendUint32(authData, a.count())
	client := a.clientData("webauthn.get", o.PublicKey.Challenge)
	hash := sha256.Sum256(client)
	key := a.key
	if a.WrongKey {
		key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	digest := sha256.Sum256(append(append([]byte{}, authData...), hash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return mustJSON(t, map[string]any{
		"id": b64.EncodeToString(a.CredID), "rawId": b64.EncodeToString(a.CredID), "type": "public-key",
		"response": map[string]any{
			"clientDataJSON": b64.EncodeToString(client), "authenticatorData": b64.EncodeToString(authData),
			"signature": b64.EncodeToString(sig), "userHandle": b64.EncodeToString(a.userID),
		},
	})
}

func (a *Authenticator) clientData(kind, challenge string) []byte {
	out, _ := json.Marshal(map[string]any{"type": kind, "challenge": challenge, "origin": a.Origin, "crossOrigin": false})
	return out
}

func mustJSON(t testing.TB, v any) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
