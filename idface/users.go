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
