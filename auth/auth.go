package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

type GoogleTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

func GetGoogleAccessToken(clientID, clientSecret, refreshToken string) (string, error) {
	data := map[string]string{
		"client_id":     clientID,
		"client_secret": clientSecret,
		"refresh_token": refreshToken,
		"grant_type":    "refresh_token",
	}
	body, _ := json.Marshal(data)
	req, err := http.NewRequest("POST", "https://oauth2.googleapis.com/token", bytes.NewBuffer(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var tokenResp GoogleTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("failed to decode token response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return "", errors.New("no access_token in response")
	}
	return tokenResp.AccessToken, nil
}
