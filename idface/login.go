package idface

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"idface-sync/config"
)

func Login() (string, error) {

	payload := map[string]string{
		"login":    config.LOGIN,
		"password": config.PASSWORD,
	}

	jsonData, _ := json.Marshal(payload)

	resp, err := http.Post(
		fmt.Sprintf("http://%s/login.fcgi", config.IDFACE_IP),
		"application/json",
		bytes.NewBuffer(jsonData),
	)

	if err != nil {
		return "", err
	}

	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result map[string]interface{}

	json.Unmarshal(body, &result)

	session := result["session"].(string)

	return session, nil
}
