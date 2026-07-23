package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

type StoreRequestState string

const (
	StoreUnscanned         StoreRequestState = ""
	StoreAbsent            StoreRequestState = "absent"
	StoreExplicitFalse     StoreRequestState = "explicit_false"
	StoreExplicitTrue      StoreRequestState = "explicit_true"
	StoreNull              StoreRequestState = "null"
	StoreInvalid           StoreRequestState = "invalid"
	StoreScanLimitExceeded StoreRequestState = "scan_limit_exceeded"
)

type StoreRequestFact struct {
	State StoreRequestState
}

type RequestMetadata struct {
	Store StoreRequestFact
}

func ScanRequestMetadata(body []byte, maxBytes int) RequestMetadata {
	if maxBytes < 1 || len(body) > maxBytes {
		return RequestMetadata{Store: StoreRequestFact{State: StoreScanLimitExceeded}}
	}
	if len(body) == 0 || !utf8.Valid(body) {
		return invalidRequestMetadata()
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return invalidRequestMetadata()
	}
	state := StoreAbsent
	storeSeen := false
	invalid := false
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return invalidRequestMetadata()
		}
		name, ok := key.(string)
		if !ok {
			return invalidRequestMetadata()
		}
		if name != "store" {
			if skipJSONValue(decoder) != nil {
				return invalidRequestMetadata()
			}
			continue
		}
		if storeSeen {
			invalid = true
		}
		storeSeen = true
		value, err := decoder.Token()
		if err != nil {
			return invalidRequestMetadata()
		}
		switch typed := value.(type) {
		case bool:
			if typed {
				state = StoreExplicitTrue
			} else {
				state = StoreExplicitFalse
			}
		case nil:
			state = StoreNull
		case json.Delim:
			invalid = true
			if skipOpenedJSONValue(decoder, typed) != nil {
				return invalidRequestMetadata()
			}
		default:
			invalid = true
		}
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return invalidRequestMetadata()
	}
	if _, err := decoder.Token(); err != io.EOF {
		return invalidRequestMetadata()
	}
	if invalid {
		state = StoreInvalid
	}
	return RequestMetadata{Store: StoreRequestFact{State: state}}
}

func (m RequestMetadata) Apply(shape RequestShape) RequestShape {
	shape.StoreRequest = m.Store
	return shape
}

func invalidRequestMetadata() RequestMetadata {
	return RequestMetadata{Store: StoreRequestFact{State: StoreInvalid}}
}

func skipJSONValue(decoder *json.Decoder) error {
	value, err := decoder.Token()
	if err != nil {
		return err
	}
	if opened, ok := value.(json.Delim); ok {
		return skipOpenedJSONValue(decoder, opened)
	}
	return nil
}

func skipOpenedJSONValue(decoder *json.Decoder, opened json.Delim) error {
	if opened != '{' && opened != '[' {
		return nil
	}
	for decoder.More() {
		if opened == '{' {
			if _, err := decoder.Token(); err != nil {
				return err
			}
		}
		if err := skipJSONValue(decoder); err != nil {
			return err
		}
	}
	_, err := decoder.Token()
	return err
}
