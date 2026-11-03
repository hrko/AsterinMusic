//go:build daveinterop && cgo

package davecheck

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/binary"
	"runtime"
	"testing"

	"github.com/disgoorg/godave/libdave"
	"github.com/thomas-vilte/dave-go/frame"
	"github.com/thomas-vilte/dave-go/mediakeys"
	mls "github.com/thomas-vilte/mls-go"
	"github.com/thomas-vilte/mls-go/ciphersuite"
	"github.com/thomas-vilte/mls-go/credentials"
	"github.com/thomas-vilte/mls-go/framing"
	"github.com/thomas-vilte/mls-go/group"
	memory "github.com/thomas-vilte/mls-go/storage/memory"
)

// A Go MLS peer invites official libdave into a group. Both implementations
// must then agree on derived media keys and authenticated Opus wire frames.
func TestOfficialLibdaveInterop(t *testing.T) {
	ctx := context.Background()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := key.PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	external := append([]byte{0x40, 65}, pub.Bytes()...)
	external = append(external, credentials.NewBasicCredentialFromUint64(1).Marshal()...)
	native := libdave.NewSession("", "")
	native.Init(1, 42, "1001")
	native.SetExternalSender(external)
	store := memory.NewStore()
	identity := make([]byte, 8)
	binary.BigEndian.PutUint64(identity, 1002)
	peer, err := mls.NewClient(identity, ciphersuite.MLS128DHKEMP256, mls.WithStorage(store, store), mls.WithCacheStrategy(mls.CacheNone))
	if err != nil {
		t.Fatal(err)
	}
	kp, err := peer.FreshKeyPackageBytes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	channel := make([]byte, 8)
	binary.BigEndian.PutUint64(channel, 42)
	gid, err := peer.CreateGroupWithExternalSender(ctx, channel, kp, external)
	if err != nil {
		t.Fatal(err)
	}
	_, welcome, err := peer.InviteMember(ctx, gid, native.GetMarshalledKeyPackage())
	if err != nil {
		t.Fatal(err)
	}
	message, err := framing.UnmarshalMLSMessage(welcome)
	if err != nil {
		t.Fatal(err)
	}
	if native.ProcessWelcome(message.Welcome, []string{"1001", "1002"}) == nil {
		t.Fatal("official libdave rejected Go MLS welcome")
	}
	encoded, err := store.LoadGroupState(ctx, group.NewGroupID(gid))
	if err != nil {
		t.Fatal(err)
	}
	state, err := group.UnmarshalGroupState(encoded)
	if err != nil {
		t.Fatal(err)
	}
	ratchet := func(id uint64) *mediakeys.KeyRatchet {
		secret, e := mediakeys.ExportWithMLSExporterSecret(state.EpochSecrets().ExporterSecret, state.CipherSuite(), mediakeys.ExporterLabel, mediakeys.SenderIDContextLE(id), 16)
		if e != nil {
			t.Fatal(e)
		}
		r, e := mediakeys.NewKeyRatchet(secret)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	goNativeKey, err := ratchet(1001).GetKey(0)
	if err != nil {
		t.Fatal(err)
	}
	goOwnKey, err := ratchet(1002).GetKey(0)
	if err != nil {
		t.Fatal(err)
	}
	nativeSend := native.GetKeyRatchet("1001")
	nativeReceive := native.GetKeyRatchet("1002")
	enc := libdave.NewEncryptor()
	enc.SetKeyRatchet(nativeSend)
	enc.SetPassthroughMode(false)
	enc.AssignSsrcToCodec(7, libdave.CodecOpus)
	dec := libdave.NewDecryptor()
	dec.TransitionToKeyRatchet(nativeReceive)
	dec.TransitionToPassthroughMode(false)
	for i := 1; i <= 1000; i++ {
		plain := make([]byte, 3+i%350)
		plain[0] = 0xf8
		for j := 1; j < len(plain); j++ {
			plain[j] = byte(j + i)
		}
		out := make([]byte, enc.GetMaxCiphertextByteSize(libdave.MediaTypeAudio, len(plain)))
		n, e := enc.Encrypt(libdave.MediaTypeAudio, 7, plain, out)
		if e != nil {
			t.Fatal(e)
		}
		restored, _, e := frame.Decrypt(frame.DecryptParams{Ciphertext: out[:n], Key: goNativeKey})
		if e != nil || !bytes.Equal(restored, plain) {
			t.Fatalf("native -> Go frame %d: %v", i, e)
		}
		forged := append([]byte(nil), out[:n]...)
		forged[0] ^= 1
		if _, _, e = frame.Decrypt(frame.DecryptParams{Ciphertext: forged, Key: goNativeKey}); e == nil {
			t.Fatal("Go accepted tampering")
		}
		wire, e := frame.Encrypt(frame.EncryptParams{Plaintext: plain, Key: goOwnKey, TruncatedNonce: uint32(i)})
		if e != nil {
			t.Fatal(e)
		}
		output := make([]byte, len(wire))
		n, e = dec.Decrypt(libdave.MediaTypeAudio, wire, output)
		if e != nil || !bytes.Equal(output[:n], plain) {
			t.Fatalf("Go -> native frame %d: %v", i, e)
		}
		forged = append([]byte(nil), wire...)
		forged[0] ^= 1
		if _, e = dec.Decrypt(libdave.MediaTypeAudio, forged, output); e == nil {
			t.Fatal("native accepted tampering")
		}
	}
	runtime.KeepAlive(nativeSend)
	runtime.KeepAlive(nativeReceive)
	runtime.KeepAlive(native)
	t.Log("Go MLS welcome accepted; 1000 frames each direction and tampering rejection passed")
}
