package handlers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"idface-sync/config"
	"idface-sync/database"
	"idface-sync/idface"
	"idface-sync/models"
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

// deleteUserFromDevice remove completamente o usuário do dispositivo
func deleteUserFromDevice(userID int) error {
	url := fmt.Sprintf("http://%s/destroy_objects.fcgi?session=%s", config.IDFACE_IP, session)
	payload := map[string]interface{}{
		"object": "users",
		"values": []map[string]interface{}{
			{"id": userID},
		},
	}
	jsonData, _ := json.Marshal(payload)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("falha ao remover usuário %d: status %d", userID, resp.StatusCode)
	}
	fmt.Printf("🗑️ Usuário %d removido do dispositivo\n", userID)
	return nil
}

// recreateUser recria o usuário no dispositivo (usado apenas se necessário)
func recreateUser(userID int) error {
	var user models.User
	var fotoBytes []byte
	err := database.DB.QueryRow(`
		SELECT cod_usuario, nome, matricula, foto
		FROM usuarios
		WHERE cod_usuario = $1
	`, userID).Scan(&user.ID, &user.Name, &user.Registration, &fotoBytes)
	if err != nil {
		return fmt.Errorf("erro ao buscar dados do usuário %d: %v", userID, err)
	}

	if err := idface.CreateOrModifyUser(session, user); err != nil {
		return fmt.Errorf("erro ao recriar usuário %d: %v", userID, err)
	}
	if len(fotoBytes) > 0 {
		if err := idface.SetUserImage(session, user.ID, fotoBytes); err != nil {
			fmt.Printf("⚠️ Erro ao reenviar foto do usuário %d: %v\n", userID, err)
		}
	}
	if err := idface.AddUserToAccessRule(session, user.ID, 1); err != nil {
		if !strings.Contains(err.Error(), "UNIQUE constraint failed") {
			fmt.Printf("⚠️ Erro ao reassociar regra de acesso: %v\n", err)
		}
	}
	fmt.Printf("♻️ Usuário %d recriado no dispositivo\n", userID)
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
				UserID     string `json:"user_id"`
				PortalID   string `json:"portal_id"`
				Confidence string `json:"confidence"`
			} `json:"values"`
		} `json:"object_changes"`
	}

	if err := json.Unmarshal(body, &data); err != nil {
		fmt.Printf("Erro ao parsear monitor/dao: %v\n", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	for _, change := range data.ObjectChanges {
		if change.Object == "access_logs" && change.Type == "inserted" {
			userID, _ := strconv.Atoi(change.Values.UserID)
			portalID, _ := strconv.Atoi(change.Values.PortalID)
			confidence, _ := strconv.Atoi(change.Values.Confidence)

			eventType := "unknown"
			if portalID == 1 {
				eventType = "entry"
			} else if portalID == 2 {
				eventType = "exit"
			}

			// Insere log
			_, err := database.DB.Exec(`
				INSERT INTO access_log (user_id, portal_id, event_time, confidence, event_type)
				VALUES ($1, $2, NOW(), $3, $4)
			`, userID, portalID, confidence, eventType)
			if err != nil {
				fmt.Printf("Erro ao inserir access_log: %v\n", err)
			}

			// Busca status atual
			var currentStatus string
			err = database.DB.QueryRow("SELECT status FROM user_session WHERE user_id = $1", userID).Scan(&currentStatus)
			if err == sql.ErrNoRows {
				currentStatus = "outside"
			} else if err != nil {
				fmt.Printf("Erro ao consultar status: %v\n", err)
				continue
			}

			if portalID == 1 { // ENTRADA
				if currentStatus == "outside" {
					// Verifica se ainda existe consumo não usado para hoje
					var hasUnused bool
					err = database.DB.QueryRow(`
						SELECT EXISTS(
							SELECT 1 FROM consumo_controle
							WHERE user_id = $1 AND data_pedido = CURRENT_DATE AND entrada_ocorrida = FALSE
						)
					`, userID).Scan(&hasUnused)
					if err != nil {
						fmt.Printf("Erro ao verificar consumo: %v\n", err)
						continue
					}
					if !hasUnused {
						fmt.Printf("⛔ Entrada bloqueada: user %d não possui consumo válido para hoje\n", userID)
						continue
					}

					// Atualiza status no banco para inside
					_, err = database.DB.Exec(`
						INSERT INTO user_session (user_id, status, last_event_time, last_portal_id)
						VALUES ($1, 'inside', NOW(), $2)
						ON CONFLICT (user_id) DO UPDATE SET
							status = 'inside',
							last_event_time = NOW(),
							last_portal_id = $2
					`, userID, portalID)
					if err != nil {
						fmt.Printf("Erro ao atualizar status (entrada): %v\n", err)
					} else {
						fmt.Printf("✅ Entrada registrada: user %d\n", userID)

						// MARCA entrada_ocorrida = TRUE
						res, err := database.DB.Exec(`
							UPDATE consumo_controle
							SET entrada_ocorrida = TRUE
							WHERE user_id = $1 AND data_pedido = CURRENT_DATE AND entrada_ocorrida = FALSE
						`, userID)
						if err != nil {
							fmt.Printf("Erro ao marcar entrada_ocorrida: %v\n", err)
						} else {
							rowsAffected, _ := res.RowsAffected()
							fmt.Printf("✅ entrada_ocorrida atualizado para user %d. Linhas afetadas: %d\n", userID, rowsAffected)
						}
					}
				} else {
					fmt.Printf("⛔ Tentativa de entrada bloqueada: user %d já está inside\n", userID)
				}
			} else if portalID == 2 { // SAÍDA
				if currentStatus == "inside" {
					// Atualiza status no banco para outside
					_, err = database.DB.Exec(`
						UPDATE user_session SET status = 'outside', last_event_time = NOW(), last_portal_id = $1
						WHERE user_id = $2
					`, portalID, userID)
					if err != nil {
						fmt.Printf("Erro ao atualizar status (saída): %v\n", err)
					} else {
						fmt.Printf("🚪 Saída registrada: user %d. Removendo usuário do dispositivo.\n", userID)
						go deleteUserFromDevice(userID)
					}
				} else {
					fmt.Printf("⚠️ Saída ignorada: user %d já estava fora\n", userID)
				}
			}
		}
	}

	w.WriteHeader(http.StatusNoContent)
	fmt.Println("Monitor/dao processado")
}

// handleUserIdentified (mantido, mas não usado no modo Monitor)
func handleUserIdentified(w http.ResponseWriter, body []byte) {
	values, err := url.ParseQuery(string(body))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	userMatricula := values.Get("registration")
	userName := values.Get("user_name")
	portalID, _ := strconv.Atoi(values.Get("portal_id"))

	var userID int
	err = database.DB.QueryRow(`SELECT cod_usuario FROM usuarios WHERE matricula = $1`, userMatricula).Scan(&userID)
	if err != nil {
		if err == sql.ErrNoRows {
			fmt.Printf("❌ Matrícula %s não encontrada.\n", userMatricula)
		} else {
			fmt.Printf("Erro ao buscar usuário: %v\n", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "denied", "reason": "Usuário não cadastrado"})
		return
	}

	// Verifica se existe consumo não utilizado para hoje
	var hasUnused bool
	err = database.DB.QueryRow(`
		SELECT EXISTS(
			SELECT 1 FROM consumo_controle
			WHERE user_id = $1 AND data_pedido = CURRENT_DATE AND entrada_ocorrida = FALSE
		)
	`, userID).Scan(&hasUnused)
	if err != nil {
		fmt.Printf("Erro ao verificar consumo: %v\n", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if !hasUnused {
		fmt.Printf("❌ Acesso NEGADO: %s (ID %d) não possui consumo válido para hoje\n", userName, userID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "denied", "reason": "Não autorizado hoje"})
		return
	}

	var status string
	err = database.DB.QueryRow("SELECT status FROM user_session WHERE user_id = $1", userID).Scan(&status)
	if err == sql.ErrNoRows {
		status = "outside"
	} else if err != nil {
		fmt.Printf("Erro ao consultar status: %v\n", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	allowed := false
	newStatus := status
	if portalID == 1 && status == "outside" {
		allowed = true
		newStatus = "inside"
		fmt.Printf("✅ Entrada permitida: %s (ID %d)\n", userName, userID)
	} else if portalID == 2 {
		allowed = true
		if status == "inside" {
			newStatus = "outside"
			fmt.Printf("🚪 Saída registrada: %s (ID %d)\n", userName, userID)
		} else {
			fmt.Printf("⚠️ Saída ignorada (já fora): %s\n", userName)
		}
	} else {
		allowed = true
	}

	if allowed {
		if newStatus != status {
			_, err = database.DB.Exec(`
				INSERT INTO user_session (user_id, status, last_portal_id, last_event_time)
				VALUES ($1, $2, $3, NOW())
				ON CONFLICT (user_id) DO UPDATE SET status=$2, last_portal_id=$3, last_event_time=NOW()
			`, userID, newStatus, portalID)
			if err != nil {
				fmt.Printf("Erro ao atualizar status: %v\n", err)
			}
			// Se for entrada, marca consumo utilizado
			if portalID == 1 {
				_, err = database.DB.Exec(`
					UPDATE consumo_controle
					SET entrada_ocorrida = TRUE
					WHERE user_id = $1 AND data_pedido = CURRENT_DATE AND entrada_ocorrida = FALSE
				`, userID)
				if err != nil {
					fmt.Printf("Erro ao marcar consumo utilizado: %v\n", err)
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "allowed"})
	} else {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "denied", "reason": "Usuário já está dentro"})
	}
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
