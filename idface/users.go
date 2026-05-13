package idface

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"idface-sync/config"
	"idface-sync/models"
)

func CreateOrModifyUser(session string, user models.User) error {

	url := fmt.Sprintf(
		"http://%s/create_or_modify_objects.fcgi?session=%s",
		config.IDFACE_IP,
		session,
	)

	payload := map[string]interface{}{
		"object": "users",
		"values": []models.User{
			user,
		},
	}

	jsonData, _ := json.Marshal(payload)

	fmt.Println("ENVIANDO:")
	fmt.Println(string(jsonData))

	resp, err := http.Post(
		url,
		"application/json",
		bytes.NewBuffer(jsonData),
	)

	if err != nil {
		return err
	}

	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	fmt.Println("RESPOSTA:")
	fmt.Println(string(body))

	return nil
}

func SetUserImage(session string, userID int, imageBytes []byte) error {

	timestamp := time.Now().Unix()

	url := fmt.Sprintf(
		"http://%s/user_set_image.fcgi?user_id=%d&match=1&timestamp=%d&session=%s",
		config.IDFACE_IP,
		userID,
		timestamp,
		session,
	)

	req, err := http.NewRequest(
		"POST",
		url,
		bytes.NewBuffer(imageBytes),
	)

	if err != nil {
		return err
	}

	req.Header.Set(
		"Content-Type",
		"application/octet-stream",
	)

	client := &http.Client{}

	resp, err := client.Do(req)

	if err != nil {
		return err
	}

	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	fmt.Println("RESPOSTA FOTO:")
	fmt.Println(string(body))

	return nil
}

// AddUserToAccessRule associa um usuário a uma regra de acesso.
// Geralmente a regra de ID 1 é a padrão que permite a passagem.
func AddUserToAccessRule(session string, userID int, ruleID int) error {
	url := fmt.Sprintf("http://%s/create_objects.fcgi?session=%s", config.IDFACE_IP, session)

	payload := map[string]interface{}{
		"object": "user_access_rules",
		"values": []map[string]interface{}{
			{
				"user_id":        userID,
				"access_rule_id": ruleID,
			},
		},
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	fmt.Printf("Regra de acesso %d associada ao usuário %d\n", ruleID, userID)
	return nil
}
