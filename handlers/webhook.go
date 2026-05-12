package handlers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"idface-sync/config"
	"idface-sync/database"
	"idface-sync/idface"
)

var eventQueue = make(chan int, 1000)
var session string

func SetSession(s string) { session = s }

func StartWorker() {
	go func() {
		for portalID := range eventQueue {
			idface.Enqueue(func() {
				_ = idface.LiberarCatraca(session, portalID)
				fmt.Println("CATRACA LIBERADA")
			})
		}
	}()
}

// Desabilita um usuário no dispositivo (impede reconhecimento)
func disableUserOnDevice(userID int) error {
	url := fmt.Sprintf("http://%s/update_objects.fcgi?session=%s", config.IDFACE_IP, session)
	payload := map[string]interface{}{
		"object": "users",
		"values": []map[string]interface{}{
			{
				"id":      userID,
				"enabled": "0",
			},
		},
	}
	jsonData, _ := json.Marshal(payload)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("falha ao desabilitar usuário: %d", resp.StatusCode)
	}
	fmt.Printf("Usuário %d desabilitado no dispositivo\n", userID)
	return nil
}

// Habilita um usuário no dispositivo
func enableUserOnDevice(userID int) error {
	url := fmt.Sprintf("http://%s/update_objects.fcgi?session=%s", config.IDFACE_IP, session)
	payload := map[string]interface{}{
		"object": "users",
		"values": []map[string]interface{}{
			{
				"id":      userID,
				"enabled": "1",
			},
		},
	}
	jsonData, _ := json.Marshal(payload)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("falha ao habilitar usuário: %d", resp.StatusCode)
	}
	fmt.Printf("Usuário %d habilitado no dispositivo\n", userID)
	return nil
}

func Webhook(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	fmt.Printf("\n================================\nROTA: %s\nBODY:\n%s\n================================\n", r.URL.Path, string(body))

	switch r.URL.Path {
	case "/device_is_alive.fcgi":
		w.WriteHeader(http.StatusNoContent)
		fmt.Println("RESPOSTA device_is_alive: 204 No Content")
		return
	case "/new_user_identified.fcgi":
		handleUserIdentified(w, body)
	case "/access_log.fcgi":
		handleAccessLog(w, body)
	case "/monitor/operation_mode":
		handleOperationMode(w, body)
	case "/monitor/dao":
		handleMonitorDAO(w, body)
	case "/monitor/catra_event":
		w.WriteHeader(http.StatusNoContent)
		fmt.Println("Monitor/catra_event recebido (evento de catraca)")
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func handleMonitorDAO(w http.ResponseWriter, body []byte) {
	var data struct {
		ObjectChanges []struct {
			Object string `json:"object"`
			Type   string `json:"type"`
			Values struct {
				UserID   string `json:"user_id"`
				PortalID string `json:"portal_id"`
			} `json:"values"`
		} `json:"object_changes"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		fmt.Printf("Erro ao parsear monitor/dao: %v\n", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	const ENTRADA_PORTAL = 1
	const SAIDA_PORTAL = 2

	for _, change := range data.ObjectChanges {
		if change.Object == "access_logs" && change.Type == "inserted" {
			userID, err := strconv.Atoi(change.Values.UserID)
			if err != nil {
				fmt.Printf("Erro ao converter user_id '%s': %v\n", change.Values.UserID, err)
				continue
			}
			portalID, err := strconv.Atoi(change.Values.PortalID)
			if err != nil {
				fmt.Printf("Erro ao converter portal_id '%s': %v\n", change.Values.PortalID, err)
				continue
			}

			// Busca estado atual no banco
			var status string
			err = database.DB.QueryRow("SELECT status FROM user_session WHERE user_id = $1", userID).Scan(&status)
			if err == sql.ErrNoRows {
				status = "outside"
			} else if err != nil {
				fmt.Printf("Erro ao consultar status: %v\n", err)
				continue
			}

			if portalID == ENTRADA_PORTAL {
				if status == "inside" {
					fmt.Printf("⚠️ Usuário %d tentou entrar novamente. Desabilitando no dispositivo.\n", userID)
					go disableUserOnDevice(userID)
				} else {
					_, err = database.DB.Exec(`INSERT INTO user_session (user_id, status, last_portal_id, last_event_time)
                        VALUES ($1, 'inside', $2, NOW())
                        ON CONFLICT (user_id) DO UPDATE SET status = 'inside', last_portal_id = $2, last_event_time = NOW()`,
						userID, portalID)
					if err != nil {
						fmt.Printf("Erro ao salvar entrada: %v\n", err)
					}
					go disableUserOnDevice(userID)
					fmt.Printf("🚪 Entrada registrada para usuário %d. Usuário desabilitado até a saída.\n", userID)
				}
			} else if portalID == SAIDA_PORTAL {
				_, err = database.DB.Exec(`UPDATE user_session SET status = 'outside', last_portal_id = $1, last_event_time = NOW() WHERE user_id = $2`,
					portalID, userID)
				if err != nil {
					fmt.Printf("Erro ao atualizar saída: %v\n", err)
				}
				go enableUserOnDevice(userID)
				fmt.Printf("🚪 Saída registrada para usuário %d. Usuário reabilitado.\n", userID)
			}
		}
	}

	w.WriteHeader(http.StatusNoContent)
	fmt.Println("Monitor/dao processado")
}

// handleUserIdentified só é chamado no modo Push/Enterprise (não usado no Monitor)
func handleUserIdentified(w http.ResponseWriter, body []byte) {
	// Mantido vazio pois não é utilizado no modo Monitor atual
	w.WriteHeader(http.StatusOK)
}

func handleAccessLog(w http.ResponseWriter, body []byte) {
	w.WriteHeader(http.StatusNoContent)
	fmt.Println("Access_log confirmado")
}

func handleOperationMode(w http.ResponseWriter, body []byte) {
	resp := map[string]interface{}{
		"status":      "ok",
		"server_time": time.Now().Unix(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}
