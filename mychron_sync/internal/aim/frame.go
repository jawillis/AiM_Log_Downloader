// SPDX-License-Identifier: GPL-3.0-or-later
//
// Wire format ported from mychron_wifi.py and mychron_full_api_guide.md in
// github.com/TheAngryRaven/mychron-wifi-spec (Copyright (C) 2026 TheAngryRaven,
// GPL-3.0-or-later).

// Package aim implements the subset of AiM's Wi-Fi protocol needed to list and
// download recorded sessions from a MyChron / Solo 2 DL style logger.
package aim

import (
	"encoding/binary"
	"fmt"
)

const (
	DefaultTCPPort = 2000
	DefaultUDPPort = 36002

	maxFramePayload = 1 << 20   // sanity cap on one frame
	maxTransfer     = 256 << 20 // sanity cap on one file
)

// STNC (opcode, sub-code) pairs, validated against captures in the spec.
const (
	opList  uint16 = 0x0024
	subList uint16 = 0x0002
	opRead  uint16 = 0x0002
	subRead uint16 = 0x0004
)

var (
	keepaliveMsg = []byte("aim-ka")
	helloFrame   = buildFrame("STCP", []byte{0, 0, 0, 0, 0x06, 0x08, 0, 0})
)

// frame is one decoded wire envelope.
type frame struct {
	tag        string
	payload    []byte
	checksumOK bool // trailer present and its checksum matched
}

func checksum(p []byte) uint16 {
	var s uint32
	for _, b := range p {
		s += uint32(b)
	}
	return uint16(s & 0xFFFF)
}

// buildFrame wraps payload in the <hTAG len flag> ... <TAG cksum> envelope.
func buildFrame(tag string, payload []byte) []byte {
	out := make([]byte, 0, 12+len(payload)+8)
	out = append(out, '<', 'h')
	out = append(out, tag...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(payload)))
	out = append(out, 0, '>')
	out = append(out, payload...)
	out = append(out, '<')
	out = append(out, tag...)
	out = binary.LittleEndian.AppendUint16(out, checksum(payload))
	return append(out, '>')
}

// buildSTNC builds a 64-byte command frame (layout validated against RS3).
func buildSTNC(op, sub uint16, path string, arg uint32) ([]byte, error) {
	if len(path) > 32 {
		return nil, fmt.Errorf("path %q too long for a 64-byte command", path)
	}
	body := make([]byte, 64)
	binary.LittleEndian.PutUint16(body[8:], op)
	binary.LittleEndian.PutUint16(body[10:], sub)
	binary.LittleEndian.PutUint32(body[16:], arg)
	body[24] = 1
	copy(body[32:], path)
	return buildFrame("STNC", body), nil
}

// buildAck builds the 4-byte running-offset ACK that drives chunked reads.
func buildAck(offset uint32) []byte {
	return buildFrame("STCP", binary.LittleEndian.AppendUint32(nil, offset))
}
