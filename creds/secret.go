package creds

import (
	"encoding/json"
	"time"

	"github.com/cplieger/go/masked"
)

// InstallationToken is one minted installation token and the instant GitHub
// stops honouring it.
type InstallationToken struct {
	Token  masked.Secret
	Expiry time.Time
}

// decodedSecret is a token member of an answer, masked as it is decoded so the
// answer never holds its text in clear. A JSON null or an absent member is the
// zero Secret.
type decodedSecret struct {
	masked.Secret
}

func (d *decodedSecret) UnmarshalJSON(b []byte) error {
	var text string
	if err := json.Unmarshal(b, &text); err != nil {
		return err
	}
	d.Secret = masked.NewSecret(text)
	return nil
}
