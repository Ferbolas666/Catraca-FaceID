package idface

import (
	"bytes"
	"encoding/json"
	"fmt"
	"idface-sync/config"
	"io"
	"net/http"
	"time"
)

func LiberarCatraca(session string, portalID int) error {
	url := fmt.Sprintf(
		"http://%s/execute_actions.fcgi?session=%s",
		config.IDFACE_IP,
		session,
	)

	payload := map[string]interface{}{
		"actions": []map[string]interface{}{
			{
				"action":     "sec_box",
				"parameters": fmt.Sprintf("id=%d, reason=3", portalID),
			},
		},
	}

	jsonData, _ := json.Marshal(payload)

	client := &http.Client{
		Timeout: 3 * time.Second,
	}

	resp, err := client.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Println("ACTION RESPONSE:", string(body))
	return nil
}
