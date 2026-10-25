package adversarial

import (
	"bytes"
	"io"
	"net/http"
)

// httpPostRaw performs one JSON POST and returns the raw body.
func httpPostRaw(url string, body []byte) ([]byte, error) {
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}
