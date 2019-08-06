/*
 * This file is subject to the terms and conditions defined in
 * file 'LICENSE.md', which is part of this source code package.
 */

package security

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"crypto/rc4"
	"encoding/binary"
	"errors"

	"github.com/snwfdhmp/unipdf/common"
)

var _ StdHandler = stdHandlerR4{}

const padding = "\x28\xBF\x4E\x5E\x4E\x75\x8A\x41\x64\x00\x4E\x56\xFF" +
	"\xFA\x01\x08\x2E\x2E\x00\xB6\xD0\x68\x3E\x80\x2F\x0C" +
	"\xA9\xFE\x64\x53\x69\x7A"

// NewHandlerR4 creates a new standard security handler for R<=4.
func NewHandlerR4(id0 string, length int) StdHandler {
	return stdHandlerR4{ID0: id0, Length: length}
}

// stdHandlerR4 is a standard security handler for R<=4.
// It uses RC4 and MD5 to generate encryption parameters.
// This legacy handler also requires Length parameter from
// Encrypt dictionary and ID0 from the trailer.
type stdHandlerR4 struct {
	Length int
	ID0    string
}

func (stdHandlerR4) paddedPass(pass []byte) []byte {
	key := make([]byte, 32)
	i := copy(key, pass)
	for ; i < 32; i++ {
		key[i] = padding[i-len(pass)]
	}
	return key
}

// alg2 computes an encryption key.
func (sh stdHandlerR4) alg2(d *StdEncryptDict, pass []byte) []byte {
	common.Log.Trace("alg2")
	key := sh.paddedPass(pass)

	h := md5.New()
	h.Write(key)

	// Pass O.
	h.Write(d.O)

	// Pass P (Lower order byte first).
	var pb [4]byte
	binary.LittleEndian.PutUint32(pb[:], uint32(d.P))
	h.Write(pb[:])
	common.Log.Trace("go P: % x", pb)

	// Pass ID[0] from the trailer
	h.Write([]byte(sh.ID0))

	common.Log.Trace("this.R = %d encryptMetadata %v", d.R, d.EncryptMetadata)
	if (d.R >= 4) && !d.EncryptMetadata {
		h.Write([]byte{0xff, 0xff, 0xff, 0xff})
	}
	hashb := h.Sum(nil)

	if d.R >= 3 {
		h = md5.New()
		for i := 0; i < 50; i++ {
			h.Reset()
			h.Write(hashb[0 : sh.Length/8])
			hashb = h.Sum(nil)
		}
	}

	if d.R >= 3 {
		return hashb[0 : sh.Length/8]
	}

	return hashb[0:5]
}

// Create the RC4 encryption key.
func (sh stdHandlerR4) alg3Key(R int, pass []byte) []byte {
	h := md5.New()
	okey := sh.paddedPass(pass)
	h.Write(okey)

	if R >= 3 {
		for i := 0; i < 50; i++ {
			hashb := h.Sum(nil)
			h = md5.New()
			h.Write(hashb)
		}
	}

	encKey := h.Sum(nil)
	if R == 2 {
		encKey = encKey[0:5]
	} else {
		encKey = encKey[0 : sh.Length/8]
	}
	return encKey
}

// alg3 computes the encryption dictionary’s O (owner password) value.
func (sh stdHandlerR4) alg3(R int, upass, opass []byte) ([]byte, error) {
	var encKey []byte
	if len(opass) > 0 {
		encKey = sh.alg3Key(R, opass)
	} else {
		encKey = sh.alg3Key(R, upass)
	}

	ociph, err := rc4.NewCipher(encKey)
	if err != nil {
		return nil, errors.New("failed rc4 ciph")
	}

	ukey := sh.paddedPass(upass)
	encrypted := make([]byte, len(ukey))
	ociph.XORKeyStream(encrypted, ukey)

	if R >= 3 {
		encKey2 := make([]byte, len(encKey))
		for i := 0; i < 19; i++ {
			for j := 0; j < len(encKey); j++ {
				encKey2[j] = encKey[j] ^ byte(i+1)
			}
			ciph, err := rc4.NewCipher(encKey2)
			if err != nil {
				return nil, errors.New("failed rc4 ciph")
			}
			ciph.XORKeyStream(encrypted, encrypted)
		}
	}
	return encrypted, nil
}

// alg4 computes the encryption dictionary’s U (user password) value (Security handlers of revision 2).
func (sh stdHandlerR4) alg4(ekey []byte, upass []byte) ([]byte, error) {
	ciph, err := rc4.NewCipher(ekey)
	if err != nil {
		return nil, errors.New("failed rc4 ciph")
	}

	s := []byte(padding)
	encrypted := make([]byte, len(s))
	ciph.XORKeyStream(encrypted, s)
	return encrypted, nil
}

// alg5 computes the encryption dictionary’s U (user password) value (Security handlers of revision 3 or greater).
func (sh stdHandlerR4) alg5(ekey []byte, upass []byte) ([]byte, error) {
	h := md5.New()
	h.Write([]byte(padding))
	h.Write([]byte(sh.ID0))
	hash := h.Sum(nil)

	common.Log.Trace("alg5")
	common.Log.Trace("ekey: % x", ekey)
	common.Log.Trace("ID: % x", sh.ID0)

	if len(hash) != 16 {
		return nil, errors.New("hash length not 16 bytes")
	}

	ciph, err := rc4.NewCipher(ekey)
	if err != nil {
		return nil, errors.New("failed rc4 ciph")
	}
	encrypted := make([]byte, 16)
	ciph.XORKeyStream(encrypted, hash)

	// Do the following 19 times: Take the output from the previous
	// invocation of the RC4 function and pass it as input to a new
	// invocation of the function; use an encryption key generated by
	// taking each byte of the original encryption key obtained in step
	// (a) and performing an XOR (exclusive or) operation between that
	// byte and the single-byte value of the iteration counter (from 1 to 19).
	ekey2 := make([]byte, len(ekey))
	for i := 0; i < 19; i++ {
		for j := 0; j < len(ekey); j++ {
			ekey2[j] = ekey[j] ^ byte(i+1)
		}
		ciph, err = rc4.NewCipher(ekey2)
		if err != nil {
			return nil, errors.New("failed rc4 ciph")
		}
		ciph.XORKeyStream(encrypted, encrypted)
		common.Log.Trace("i = %d, ekey: % x", i, ekey2)
		common.Log.Trace("i = %d -> % x", i, encrypted)
	}

	bb := make([]byte, 32)
	for i := 0; i < 16; i++ {
		bb[i] = encrypted[i]
	}

	// Append 16 bytes of arbitrary padding to the output from the final
	// invocation of the RC4 function and store the 32-byte result as
	// the value of the U entry in the encryption dictionary.
	_, err = rand.Read(bb[16:32])
	if err != nil {
		return nil, errors.New("failed to gen rand number")
	}
	return bb, nil
}

// alg6 authenticates the user password and returns the document encryption key.
// It returns an nil key in case authentication failed.
func (sh stdHandlerR4) alg6(d *StdEncryptDict, upass []byte) ([]byte, error) {
	var (
		uo  []byte
		err error
	)
	ekey := sh.alg2(d, upass)
	if d.R == 2 {
		uo, err = sh.alg4(ekey, upass)
	} else if d.R >= 3 {
		uo, err = sh.alg5(ekey, upass)
	} else {
		return nil, errors.New("invalid R")
	}
	if err != nil {
		return nil, err
	}

	common.Log.Trace("check: % x == % x ?", string(uo), string(d.U))

	uGen := uo  // Generated U from specified pass.
	uDoc := d.U // U from the document.
	if d.R >= 3 {
		// comparing on the first 16 bytes in the case of security
		// handlers of revision 3 or greater),
		if len(uGen) > 16 {
			uGen = uGen[0:16]
		}
		if len(uDoc) > 16 {
			uDoc = uDoc[0:16]
		}
	}

	if !bytes.Equal(uGen, uDoc) {
		return nil, nil
	}
	return ekey, nil
}

// alg7 authenticates the owner password and returns the document encryption key.
// It returns an nil key in case authentication failed.
func (sh stdHandlerR4) alg7(d *StdEncryptDict, opass []byte) ([]byte, error) {
	encKey := sh.alg3Key(d.R, opass)

	decrypted := make([]byte, len(d.O))
	if d.R == 2 {
		ciph, err := rc4.NewCipher(encKey)
		if err != nil {
			return nil, errors.New("failed cipher")
		}
		ciph.XORKeyStream(decrypted, d.O)
	} else if d.R >= 3 {
		s := append([]byte{}, d.O...)
		for i := 0; i < 20; i++ {
			//newKey := encKey
			newKey := append([]byte{}, encKey...)
			for j := 0; j < len(encKey); j++ {
				newKey[j] ^= byte(19 - i)
			}
			ciph, err := rc4.NewCipher(newKey)
			if err != nil {
				return nil, errors.New("failed cipher")
			}
			ciph.XORKeyStream(decrypted, s)
			s = append([]byte{}, decrypted...)
		}
	} else {
		return nil, errors.New("invalid R")
	}

	ekey, err := sh.alg6(d, decrypted)
	if err != nil {
		// TODO(dennwc): this doesn't look right, but it was in the old code
		return nil, nil
	}
	return ekey, nil
}

// GenerateParams generates and sets O and U parameters for the encryption dictionary.
// It expects R, P and EncryptMetadata fields to be set.
func (sh stdHandlerR4) GenerateParams(d *StdEncryptDict, opass, upass []byte) ([]byte, error) {
	// Make the O and U objects.
	O, err := sh.alg3(d.R, upass, opass)
	if err != nil {
		common.Log.Debug("ERROR: Error generating O for encryption (%s)", err)
		return nil, err
	}
	d.O = O
	common.Log.Trace("gen O: % x", O)
	// requires O
	ekey := sh.alg2(d, upass)

	U, err := sh.alg5(ekey, upass)
	if err != nil {
		common.Log.Debug("ERROR: Error generating O for encryption (%s)", err)
		return nil, err
	}
	d.U = U
	common.Log.Trace("gen U: % x", U)
	return ekey, nil
}

// Authenticate implements StdHandler interface.
func (sh stdHandlerR4) Authenticate(d *StdEncryptDict, pass []byte) ([]byte, Permissions, error) {
	// Try owner password.
	// May not be necessary if only want to get all contents.
	// (user pass needs to be known or empty).
	common.Log.Trace("Debugging authentication - owner pass")
	ekey, err := sh.alg7(d, pass)
	if err != nil {
		return nil, 0, err
	}
	if ekey != nil {
		common.Log.Trace("this.authenticated = True")
		return ekey, PermOwner, nil
	}

	// Try user password.
	common.Log.Trace("Debugging authentication - user pass")
	ekey, err = sh.alg6(d, pass)
	if err != nil {
		return nil, 0, err
	}
	if ekey != nil {
		common.Log.Trace("this.authenticated = True")
		return ekey, d.P, nil
	}
	// Cannot even view the file.
	return nil, 0, nil
}
