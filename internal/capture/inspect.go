package capture

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"time"
)

// ParseTLSClientHello 从首包明文字节中无侵入嗅探 TLS ClientHello 元信息（SNI、协议版本、ALPN）。
func ParseTLSClientHello(data []byte) (sni, version, alpn string, ok bool) {
	// TLS Record Header 至少 5 字节：Type(1)=0x16(Handshake), LegacyVersion(2), Length(2)
	if len(data) < 9 || data[0] != 0x16 || data[1] != 0x03 {
		return "", "", "", false
	}
	recLen := int(binary.BigEndian.Uint16(data[3:5]))
	end := min(len(data), 5+recLen)
	payload := data[5:end]
	return parseTLSClientHelloHandshake(payload)
}

// parseTLSClientHelloHandshake 解析 TLS Handshake 消息体（从 HandshakeType=0x01 开始，不含 5 字节 Record 头）。
func parseTLSClientHelloHandshake(payload []byte) (sni, version, alpn string, ok bool) {
	if len(payload) < 42 || payload[0] != 0x01 {
		return "", "", "", false
	}
	hsLen := int(payload[1])<<16 | int(payload[2])<<8 | int(payload[3])
	msgEnd := min(len(payload), 4+hsLen)
	msg := payload[4:msgEnd]
	if len(msg) < 38 {
		return "", "", "", false
	}

	legacyVer := binary.BigEndian.Uint16(msg[0:2])
	version = formatTLSVersion(legacyVer)
	pos := 34 // 2 bytes version + 32 bytes random

	// Session ID
	if pos >= len(msg) {
		return "", version, "", true
	}
	sidLen := int(msg[pos])
	pos += 1 + sidLen
	if pos+2 > len(msg) {
		return "", version, "", true
	}

	// Cipher Suites
	csLen := int(binary.BigEndian.Uint16(msg[pos : pos+2]))
	pos += 2 + csLen
	if pos >= len(msg) {
		return "", version, "", true
	}

	// Compression Methods
	compLen := int(msg[pos])
	pos += 1 + compLen
	if pos+2 > len(msg) {
		return "", version, "", true
	}

	// Extensions
	extTotal := int(binary.BigEndian.Uint16(msg[pos : pos+2]))
	pos += 2
	extEnd := min(len(msg), pos+extTotal)
	var alpnList []string

	for pos+4 <= extEnd {
		extType := binary.BigEndian.Uint16(msg[pos : pos+2])
		extLen := int(binary.BigEndian.Uint16(msg[pos+2 : pos+4]))
		pos += 4
		if pos+extLen > extEnd {
			break
		}
		extData := msg[pos : pos+extLen]
		pos += extLen

		switch extType {
		case 0x0000: // server_name (SNI)
			if len(extData) >= 5 {
				listLen := int(binary.BigEndian.Uint16(extData[0:2]))
				p := 2
				limit := min(len(extData), 2+listLen)
				for p+3 <= limit {
					nameType := extData[p]
					nameLen := int(binary.BigEndian.Uint16(extData[p+1 : p+3]))
					p += 3
					if p+nameLen > limit {
						break
					}
					if nameType == 0x00 {
						sni = string(extData[p : p+nameLen])
						break
					}
					p += nameLen
				}
			}
		case 0x0010: // application_layer_protocol_negotiation (ALPN)
			if len(extData) >= 2 {
				listLen := int(binary.BigEndian.Uint16(extData[0:2]))
				p := 2
				limit := min(len(extData), 2+listLen)
				for p < limit {
					l := int(extData[p])
					p++
					if p+l > limit {
						break
					}
					alpnList = append(alpnList, string(extData[p:p+l]))
					p += l
				}
			}
		case 0x002b: // supported_versions
			if len(extData) >= 3 {
				l := int(extData[0])
				limit := min(len(extData), 1+l)
				var maxVer uint16
				for p := 1; p+2 <= limit; p += 2 {
					v := binary.BigEndian.Uint16(extData[p : p+2])
					// 跳过 GREASE 值（0x?a?a）
					if v&0x0f0f == 0x0a0a {
						continue
					}
					if v > maxVer {
						maxVer = v
					}
				}
				if maxVer > 0 {
					version = formatTLSVersion(maxVer)
				}
			}
		}
	}

	if len(alpnList) > 0 {
		alpn = strings.Join(alpnList, ", ")
	}
	return sni, version, alpn, true
}

func formatTLSVersion(v uint16) string {
	switch v {
	case 0x0301:
		return "TLS 1.0"
	case 0x0302:
		return "TLS 1.1"
	case 0x0303:
		return "TLS 1.2"
	case 0x0304:
		return "TLS 1.3"
	default:
		return fmt.Sprintf("TLS 0x%04x", v)
	}
}

// InspectUDPPacket 对单个 UDP 数据报进行协议识别与结构化元数据提取。
func InspectUDPPacket(dstPort int, payload []byte, direction string, now time.Time) (proto string, summary string, dnsRec *DNSRecordSummary, quicMeta *QUICMetadata) {
	if len(payload) == 0 {
		return "UDP", "空数据报", nil, nil
	}

	// 1. 尝试识别 DNS（标准 53 端口或 5353 mDNS 或符合 DNS 报文结构）
	if dstPort == 53 || dstPort == 5353 {
		if rec, ok := ParseDNSPacket(payload, now); ok {
			p := "DNS"
			if dstPort == 5353 {
				p = "mDNS"
			}
			if rec.Direction == "answer" && len(rec.Answers) > 0 {
				return p, fmt.Sprintf("响应 %s (%s) -> %s", rec.Name, rec.QType, strings.Join(rec.Answers, ", ")), &rec, nil
			}
			if rec.Direction == "answer" && rec.RCode != "" && rec.RCode != "NOERROR" {
				return p, fmt.Sprintf("响应 %s (%s) [%s]", rec.Name, rec.QType, rec.RCode), &rec, nil
			}
			return p, fmt.Sprintf("查询 %s (%s)", rec.Name, rec.QType), &rec, nil
		}
	}

	// 2. 识别 STUN / WebRTC（Magic Cookie = 0x2112A442 位于第 4..8 字节，且前两位为 00）
	if len(payload) >= 20 && payload[0]&0xc0 == 0 && binary.BigEndian.Uint32(payload[4:8]) == 0x2112A442 {
		msgType := binary.BigEndian.Uint16(payload[0:2])
		return "STUN", formatSTUNMessageType(msgType), nil, nil
	}

	// 3. 识别 QUIC（Long Header 首字节高两位为 11，支持提取 v1/v2 Initial 包内 SNI 与 ALPN）
	if qm, ok := ParseQUICInitial(payload); ok {
		sum := qm.Version + " Initial"
		if qm.SNI != "" {
			sum += " · " + qm.SNI
		}
		if qm.ALPN != "" {
			sum += " (" + qm.ALPN + ")"
		}
		return "QUIC", sum, nil, &qm
	}
	if dstPort == 443 && len(payload) >= 16 {
		return "QUIC", fmt.Sprintf("QUIC 数据报 (%d B)", len(payload)), nil, nil
	}

	// 4. 识别 NTP（UDP 123，标准 48 字节）
	if dstPort == 123 && len(payload) >= 48 {
		ver := (payload[0] >> 3) & 0x07
		mode := payload[0] & 0x07
		return "NTP", fmt.Sprintf("NTPv%d mode=%d", ver, mode), nil, nil
	}

	// 5. 兜底尝试按 DNS 探测（非 53 端口的自定义 DNS 服务）
	if rec, ok := ParseDNSPacket(payload, now); ok {
		if rec.Direction == "answer" && len(rec.Answers) > 0 {
			return "DNS", fmt.Sprintf("响应 %s (%s) -> %s", rec.Name, rec.QType, strings.Join(rec.Answers, ", ")), &rec, nil
		}
		return "DNS", fmt.Sprintf("查询 %s (%s)", rec.Name, rec.QType), &rec, nil
	}

	dirLabel := "发送"
	if direction == "in" {
		dirLabel = "接收"
	}
	return "UDP", fmt.Sprintf("%s %d B", dirLabel, len(payload)), nil, nil
}

func formatSTUNMessageType(t uint16) string {
	switch t {
	case 0x0001:
		return "Binding Request"
	case 0x0101:
		return "Binding Success Response"
	case 0x0111:
		return "Binding Error Response"
	case 0x0003:
		return "Allocate Request (TURN)"
	case 0x0103:
		return "Allocate Response (TURN)"
	default:
		return fmt.Sprintf("STUN 0x%04x", t)
	}
}

// ParseDNSPacket 解析标准 RFC 1035 DNS 报文（支持域名指针压缩）。
func ParseDNSPacket(pkt []byte, now time.Time) (DNSRecordSummary, bool) {
	if len(pkt) < 12 {
		return DNSRecordSummary{}, false
	}
	flags := binary.BigEndian.Uint16(pkt[2:4])
	qdCount := int(binary.BigEndian.Uint16(pkt[4:6]))
	anCount := int(binary.BigEndian.Uint16(pkt[6:8]))
	if qdCount == 0 || qdCount > 8 || anCount > 64 {
		return DNSRecordSummary{}, false
	}
	isResp := (flags & 0x8000) != 0
	rcode := flags & 0x000f

	off := 12
	qname, next, ok := readDNSName(pkt, off)
	if !ok || qname == "" || next+4 > len(pkt) {
		return DNSRecordSummary{}, false
	}
	qtype := binary.BigEndian.Uint16(pkt[next : next+2])
	qclass := binary.BigEndian.Uint16(pkt[next+2 : next+4])
	if qclass != 1 && qclass != 255 && qclass != 0x8001 {
		return DNSRecordSummary{}, false
	}
	off = next + 4

	// 跳过剩余问题段
	for i := 1; i < qdCount; i++ {
		_, n, valid := readDNSName(pkt, off)
		if !valid || n+4 > len(pkt) {
			return DNSRecordSummary{}, false
		}
		off = n + 4
	}

	rec := DNSRecordSummary{
		Timestamp: now,
		Direction: "query",
		Name:      qname,
		QType:     dnsTypeLabel(qtype),
	}
	if isResp {
		rec.Direction = "answer"
		rec.RCode = dnsRCodeLabel(rcode)
		for i := 0; i < anCount && off < len(pkt); i++ {
			_, n, valid := readDNSName(pkt, off)
			if !valid || n+10 > len(pkt) {
				break
			}
			rType := binary.BigEndian.Uint16(pkt[n : n+2])
			rdLen := int(binary.BigEndian.Uint16(pkt[n+8 : n+10]))
			rStart := n + 10
			if rStart+rdLen > len(pkt) {
				break
			}
			rdata := pkt[rStart : rStart+rdLen]
			off = rStart + rdLen

			switch rType {
			case 1: // A
				if len(rdata) == 4 {
					rec.Answers = append(rec.Answers, net.IP(rdata).String())
				}
			case 28: // AAAA
				if len(rdata) == 16 {
					rec.Answers = append(rec.Answers, net.IP(rdata).String())
				}
			case 5, 2, 12: // CNAME / NS / PTR
				if target, _, ok := readDNSName(pkt, rStart); ok && target != "" {
					rec.Answers = append(rec.Answers, target)
				}
			case 16: // TXT
				if len(rdata) > 1 {
					l := int(rdata[0])
					if 1+l <= len(rdata) {
						rec.Answers = append(rec.Answers, string(rdata[1:1+l]))
					}
				}
			case 65: // HTTPS
				rec.Answers = append(rec.Answers, "HTTPS-RR")
			}
			if len(rec.Answers) >= 8 {
				break
			}
		}
	}
	return rec, true
}

func readDNSName(pkt []byte, offset int) (string, int, bool) {
	if offset < 0 || offset >= len(pkt) {
		return "", 0, false
	}
	var labels []string
	cur := offset
	nextPos := 0
	jumped := false
	for steps := 0; steps < 64; steps++ {
		if cur >= len(pkt) {
			return "", 0, false
		}
		l := int(pkt[cur])
		if l == 0 {
			if !jumped {
				nextPos = cur + 1
			}
			return strings.Join(labels, "."), nextPos, true
		}
		if l&0xc0 == 0xc0 {
			if cur+1 >= len(pkt) {
				return "", 0, false
			}
			ptr := int(binary.BigEndian.Uint16(pkt[cur:cur+2]) & 0x3fff)
			if ptr >= len(pkt) {
				return "", 0, false
			}
			if !jumped {
				nextPos = cur + 2
				jumped = true
			}
			cur = ptr
			continue
		}
		if l&0xc0 != 0 || cur+1+l > len(pkt) {
			return "", 0, false
		}
		label := pkt[cur+1 : cur+1+l]
		for _, ch := range label {
			if ch < 0x20 || ch > 0x7e {
				return "", 0, false
			}
		}
		labels = append(labels, string(label))
		cur += 1 + l
	}
	return "", 0, false
}

func dnsTypeLabel(t uint16) string {
	switch t {
	case 1:
		return "A"
	case 2:
		return "NS"
	case 5:
		return "CNAME"
	case 6:
		return "SOA"
	case 12:
		return "PTR"
	case 15:
		return "MX"
	case 16:
		return "TXT"
	case 28:
		return "AAAA"
	case 33:
		return "SRV"
	case 65:
		return "HTTPS"
	case 255:
		return "ANY"
	default:
		return fmt.Sprintf("TYPE%d", t)
	}
}

func dnsRCodeLabel(code uint16) string {
	switch code {
	case 0:
		return "NOERROR"
	case 1:
		return "FORMERR"
	case 2:
		return "SERVFAIL"
	case 3:
		return "NXDOMAIN"
	case 4:
		return "NOTIMP"
	case 5:
		return "REFUSED"
	default:
		return fmt.Sprintf("RCODE%d", code)
	}
}

// ParseQUICInitial 解析 QUIC v1 / v2 Long Header Initial 包，并尝试使用 RFC 9001 公开初始盐解密 CRYPTO 帧提取 SNI 与 ALPN。
func ParseQUICInitial(pkt []byte) (QUICMetadata, bool) {
	// Long Header 最小结构：Flags(1) + Version(4) + DCIDLen(1) + SCIDLen(1)
	if len(pkt) < 16 || (pkt[0]&0xc0) != 0xc0 {
		return QUICMetadata{}, false
	}
	ver := binary.BigEndian.Uint32(pkt[1:5])
	var verLabel string
	var salt []byte
	var isV2 bool
	switch ver {
	case 0x00000001:
		verLabel = "QUIC v1"
		salt = []byte{0x38, 0x76, 0x2c, 0xf7, 0xf5, 0x59, 0x34, 0xb3, 0x4d, 0x17, 0x9a, 0xe6, 0xa4, 0xc8, 0x0c, 0xad, 0xcc, 0xbb, 0x7f, 0x0a}
	case 0x6b3343cf:
		verLabel = "QUIC v2"
		salt = []byte{0x0d, 0xed, 0xe3, 0xde, 0xf7, 0x00, 0xa6, 0xdb, 0x81, 0x93, 0x81, 0xbe, 0x6e, 0x26, 0x9d, 0xcb, 0xf9, 0xbd, 0x2e, 0xd9}
		isV2 = true
	default:
		return QUICMetadata{}, false
	}

	pktType := (pkt[0] & 0x30) >> 4
	// v1: Initial=0; v2: Initial=1
	if (!isV2 && pktType != 0) || (isV2 && pktType != 1) {
		return QUICMetadata{}, false
	}

	pos := 5
	if pos >= len(pkt) {
		return QUICMetadata{}, false
	}
	dcidLen := int(pkt[pos])
	pos++
	if dcidLen > 20 || pos+dcidLen >= len(pkt) {
		return QUICMetadata{}, false
	}
	dcid := pkt[pos : pos+dcidLen]
	pos += dcidLen

	scidLen := int(pkt[pos])
	pos++
	if scidLen > 20 || pos+scidLen > len(pkt) {
		return QUICMetadata{}, false
	}
	pos += scidLen

	// Token Length (QUIC varint)
	tokenLen, n, ok := readQUICVarInt(pkt[pos:])
	if !ok || pos+n+int(tokenLen) > len(pkt) {
		return QUICMetadata{Version: verLabel}, true
	}
	pos += n + int(tokenLen)

	// Payload Length (QUIC varint)
	payloadLen, n, ok := readQUICVarInt(pkt[pos:])
	if !ok {
		return QUICMetadata{Version: verLabel}, true
	}
	pos += n
	pnOffset := pos
	if pnOffset+4+16 > len(pkt) || int(payloadLen) < 20 || pnOffset+int(payloadLen) > len(pkt) {
		return QUICMetadata{Version: verLabel}, true
	}

	meta := QUICMetadata{Version: verLabel}
	// 尝试用 RFC 9001 公开初始密钥解密 Client Initial 包提取 ClientHello 的 SNI 与 ALPN
	if sni, alpn, ok := decryptQUICInitialClientHello(pkt[:pnOffset+int(payloadLen)], pnOffset, dcid, salt, isV2); ok {
		meta.SNI = sni
		meta.ALPN = alpn
	}
	return meta, true
}

func readQUICVarInt(b []byte) (uint64, int, bool) {
	if len(b) == 0 {
		return 0, 0, false
	}
	prefix := b[0] >> 6
	length := 1 << prefix
	if len(b) < length {
		return 0, 0, false
	}
	v := uint64(b[0] & 0x3f)
	for i := 1; i < length; i++ {
		v = (v << 8) | uint64(b[i])
	}
	return v, length, true
}

func decryptQUICInitialClientHello(pkt []byte, pnOffset int, dcid, salt []byte, isV2 bool) (sni, alpn string, ok bool) {
	sampleOffset := pnOffset + 4
	if sampleOffset+16 > len(pkt) {
		return "", "", false
	}
	initialSecret := hkdfExtractSHA256(salt, dcid)
	clientInitialSecret := hkdfExpandLabelSHA256(initialSecret, "client in", 32)

	keyLabel, ivLabel, hpLabel := "quic key", "quic iv", "quic hp"
	if isV2 {
		keyLabel, ivLabel, hpLabel = "quicv2 key", "quicv2 iv", "quicv2 hp"
	}
	key := hkdfExpandLabelSHA256(clientInitialSecret, keyLabel, 16)
	iv := hkdfExpandLabelSHA256(clientInitialSecret, ivLabel, 12)
	hpKey := hkdfExpandLabelSHA256(clientInitialSecret, hpLabel, 16)

	hpBlock, err := aes.NewCipher(hpKey)
	if err != nil {
		return "", "", false
	}
	var mask [16]byte
	hpBlock.Encrypt(mask[:], pkt[sampleOffset:sampleOffset+16])

	firstByte := pkt[0] ^ (mask[0] & 0x0f)
	pnLen := int(firstByte&0x03) + 1
	if pnOffset+pnLen+16 > len(pkt) {
		return "", "", false
	}

	hdr := make([]byte, pnOffset+pnLen)
	copy(hdr, pkt[:pnOffset+pnLen])
	hdr[0] = firstByte
	var pn uint64
	for i := 0; i < pnLen; i++ {
		hdr[pnOffset+i] ^= mask[1+i]
		pn = (pn << 8) | uint64(hdr[pnOffset+i])
	}

	nonce := make([]byte, 12)
	copy(nonce, iv)
	for i := 0; i < 8; i++ {
		nonce[11-i] ^= byte(pn >> (8 * i))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", "", false
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", false
	}
	plaintext, err := aead.Open(nil, nonce, pkt[pnOffset+pnLen:], hdr)
	if err != nil {
		return "", "", false
	}

	// 扫描解密后的 QUIC 帧，收集 CRYPTO 帧（Type=0x06）拼出 TLS ClientHello
	cryptoBuf := make([]byte, 0, len(plaintext))
	i := 0
	for i < len(plaintext) {
		ft := plaintext[i]
		i++
		switch ft {
		case 0x00, 0x01: // PADDING / PING
			continue
		case 0x06: // CRYPTO
			_, n1, ok1 := readQUICVarInt(plaintext[i:])
			if !ok1 {
				i = len(plaintext)
				break
			}
			i += n1
			clen, n2, ok2 := readQUICVarInt(plaintext[i:])
			if !ok2 || i+n2+int(clen) > len(plaintext) {
				i = len(plaintext)
				break
			}
			i += n2
			cryptoBuf = append(cryptoBuf, plaintext[i:i+int(clen)]...)
			i += int(clen)
		default:
			// Initial 包中非预期帧直接停止扫描
			i = len(plaintext)
		}
	}
	if len(cryptoBuf) < 42 {
		return "", "", false
	}
	sni, _, alpn, parsed := parseTLSClientHelloHandshake(cryptoBuf)
	return sni, alpn, parsed && (sni != "" || alpn != "")
}

func hkdfExtractSHA256(salt, ikm []byte) []byte {
	mac := hmac.New(sha256.New, salt)
	mac.Write(ikm)
	return mac.Sum(nil)
}

func hkdfExpandLabelSHA256(secret []byte, label string, length int) []byte {
	fullLabel := "tls13 " + label
	info := make([]byte, 0, 2+1+len(fullLabel)+1)
	info = append(info, byte(length>>8), byte(length))
	info = append(info, byte(len(fullLabel)))
	info = append(info, fullLabel...)
	info = append(info, 0) // empty context

	out := make([]byte, 0, length)
	var prev []byte
	for counter := byte(1); len(out) < length; counter++ {
		mac := hmac.New(sha256.New, secret)
		mac.Write(prev)
		mac.Write(info)
		mac.Write([]byte{counter})
		prev = mac.Sum(nil)
		out = append(out, prev...)
	}
	return out[:length]
}
