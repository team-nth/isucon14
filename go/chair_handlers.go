package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/oklog/ulid/v2"
)

type chairPostChairsRequest struct {
	Name               string `json:"name"`
	Model              string `json:"model"`
	ChairRegisterToken string `json:"chair_register_token"`
}

type chairPostChairsResponse struct {
	ID      string `json:"id"`
	OwnerID string `json:"owner_id"`
}

func chairPostChairs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req := &chairPostChairsRequest{}
	if err := bindJSON(r, req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Model == "" || req.ChairRegisterToken == "" {
		writeError(w, http.StatusBadRequest, errors.New("some of required fields(name, model, chair_register_token) are empty"))
		return
	}

	owner := &Owner{}
	if err := db.GetContext(ctx, owner, "SELECT * FROM owners WHERE chair_register_token = ?", req.ChairRegisterToken); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusUnauthorized, errors.New("invalid chair_register_token"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	chairID := ulid.Make().String()
	accessToken := secureRandomStr(32)

	_, err := db.ExecContext(
		ctx,
		"INSERT INTO chairs (id, owner_id, name, model, is_active, access_token) VALUES (?, ?, ?, ?, ?, ?)",
		chairID, owner.ID, req.Name, req.Model, false, accessToken,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Path:  "/",
		Name:  "chair_session",
		Value: accessToken,
	})

	writeJSON(w, http.StatusCreated, &chairPostChairsResponse{
		ID:      chairID,
		OwnerID: owner.ID,
	})
}

type postChairActivityRequest struct {
	IsActive bool `json:"is_active"`
}

func chairPostActivity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	chair := ctx.Value("chair").(*Chair)

	req := &postChairActivityRequest{}
	if err := bindJSON(r, req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	_, err := db.ExecContext(ctx, "UPDATE chairs SET is_active = ? WHERE id = ?", req.IsActive, chair.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type chairPostCoordinateResponse struct {
	RecordedAt int64 `json:"recorded_at"`
}

func chairPostCoordinate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	req := &Coordinate{}
	if err := bindJSON(r, req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	chair := ctx.Value("chair").(*Chair)

	tx, err := db.Beginx()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()

	// chairLocationID := ulid.Make().String()
	// if _, err := tx.ExecContext(
	// 	ctx,
	// 	`INSERT INTO chair_locations (id, chair_id, latitude, longitude) VALUES (?, ?, ?, ?)`,
	// 	chairLocationID, chair.ID, req.Latitude, req.Longitude,
	// ); err != nil {
	// 	writeError(w, http.StatusInternalServerError, err)
	// 	return
	// }

	// location := &ChairLocation{}
	// if err := tx.GetContext(ctx, location, `SELECT * FROM chair_locations WHERE id = ?`, chairLocationID); err != nil {
	// 	writeError(w, http.StatusInternalServerError, err)
	// 	return
	// }

	/*
			if _, err := db.ExecContext(ctx, "UPDATE chairs SET total_distance = ?, total_distance_updated_at = ?, latitude = ?, longitude = ? WHERE id = ?", distance, chairID2ChairLocation[chair.ID][locationLen-1].CreatedAt, chairID2ChairLocation[chair.ID][locationLen-1].Latitude, chairID2ChairLocation[chair.ID][locationLen-1].Longitude, chair.ID); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	*/
	//	if _, err := db.ExecContext(ctx, "UPDATE chairs SET latitude = ?, longitude = ?, total_distance")

	newTotalDistance := 0
	newLatitude, newLongitude := req.Latitude, req.Longitude
	if chair.Latitude.Valid && chair.Longitude.Valid {
		newTotalDistance = chair.TotalDistance + calculateDistance(int(chair.Latitude.Int32), int(chair.Longitude.Int32), newLatitude, newLongitude)
	}

	latestUpdatedAt := time.Now()
	if _, err := tx.ExecContext(ctx, "UPDATE chairs SET latitude = ?, longitude = ?, total_distance = ?, total_distance_updated_at = ? WHERE id = ?", newLatitude, newLongitude, newTotalDistance, latestUpdatedAt, chair.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	ride := &Ride{}
	if err := tx.GetContext(ctx, ride, `SELECT * FROM rides WHERE chair_id = ? ORDER BY updated_at DESC LIMIT 1`, chair.ID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	} else {
		status, err := getLatestRideStatus(ctx, tx, ride.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if status != "COMPLETED" && status != "CANCELED" {
			if req.Latitude == ride.PickupLatitude && req.Longitude == ride.PickupLongitude && status == "ENROUTE" {
				if _, err := tx.ExecContext(ctx, "INSERT INTO ride_statuses (id, ride_id, status) VALUES (?, ?, ?)", ulid.Make().String(), ride.ID, "PICKUP"); err != nil {
					writeError(w, http.StatusInternalServerError, err)
					return
				}
			}

			if req.Latitude == ride.DestinationLatitude && req.Longitude == ride.DestinationLongitude && status == "CARRYING" {
				if _, err := tx.ExecContext(ctx, "INSERT INTO ride_statuses (id, ride_id, status) VALUES (?, ?, ?)", ulid.Make().String(), ride.ID, "ARRIVED"); err != nil {
					writeError(w, http.StatusInternalServerError, err)
					return
				}
			}
		}
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, &chairPostCoordinateResponse{
		RecordedAt: latestUpdatedAt.UnixMilli(),
	})
}

type simpleUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type chairGetNotificationResponse struct {
	Data         *chairGetNotificationResponseData `json:"data"`
	RetryAfterMs int                               `json:"retry_after_ms"`
}

type chairGetNotificationResponseData struct {
	RideID                string     `json:"ride_id"`
	User                  simpleUser `json:"user"`
	PickupCoordinate      Coordinate `json:"pickup_coordinate"`
	DestinationCoordinate Coordinate `json:"destination_coordinate"`
	Status                string     `json:"status"`
}

func printAndFlush(w http.ResponseWriter, content string) {
	fmt.Fprint(w, content)

	f, ok := w.(http.Flusher)
	if !ok {
		w.Header().Set("Content-Type", "application/json;charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)

		b, _ := json.Marshal(struct {
			Error string `json:"error"`
		}{Error: "Streaming unsupported!"})

		w.Write(b)
		fmt.Fprintln(os.Stderr, "Streaming unsupported!")
		return
	}
	f.Flush()
}

func chairGetNotification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	chair := ctx.Value("chair").(*Chair)

	ride := &Ride{}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")

	defaultSleep := 2000 * time.Millisecond
	errorSleep := 10 * time.Millisecond

	maxLoop := 20
	loop := maxLoop

	for loop > 0 {
		isFirst := maxLoop == loop
		loop--

		tx, err := db.Beginx()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		defer tx.Rollback()

		if err := tx.GetContext(ctx, ride, `SELECT * FROM rides WHERE chair_id = ? ORDER BY updated_at DESC LIMIT 1`, chair.ID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				if isFirst {
					printAndFlush(w, "data: null\n\n")
				}
				time.Sleep(defaultSleep)
				continue
			} else {
				slog.Error("error SELECT rides", err)
				time.Sleep(errorSleep)
				continue
			}
		}

		var yetSentRideStatuses []RideStatus
		if rows, err := db.Queryx("SELECT * FROM ride_statuses WHERE ride_id = ? AND chair_sent_at IS NULL ORDER BY created_at ASC", ride.ID); err == nil {
			var yetSentRideStatus RideStatus
			for rows.Next() {
				if err := rows.StructScan(&yetSentRideStatus); err != nil {
					writeError(w, http.StatusInternalServerError, err)
					return
				}
				yetSentRideStatuses = append(yetSentRideStatuses, yetSentRideStatus)
			}
		} else {
			if errors.Is(err, sql.ErrNoRows) {
				if isFirst {
					status, err := getLatestRideStatus(ctx, tx, ride.ID)
					if err != nil {
						loop++ // ループ回数を戻す
						slog.Error("error getLatestRideStatus", err)
						time.Sleep(errorSleep)
						continue
					}
					var yetSentRideStatus RideStatus
					yetSentRideStatus.Status = status
					yetSentRideStatuses = append(yetSentRideStatuses)
				} else {
					time.Sleep(defaultSleep)
					continue
				}
			} else {
				slog.Error("error SELECT ride_statuses", err)
				time.Sleep(errorSleep)
				continue
			}
		}

		user := &User{}
		err = tx.GetContext(ctx, user, "SELECT * FROM users WHERE id = ? FOR SHARE", ride.UserID)
		if err != nil {
			slog.Error("error SELECT users", err)
			time.Sleep(errorSleep)
			continue
		}

		for _, yetSentRideStatus := range yetSentRideStatuses {
			if yetSentRideStatus.ID != "" {
				_, err := tx.ExecContext(ctx, `UPDATE ride_statuses SET chair_sent_at = CURRENT_TIMESTAMP(6) WHERE id = ?`, yetSentRideStatus.ID)
				if err != nil {
					slog.Error("error UPDATE ride_statuses", err)
					time.Sleep(errorSleep)
					continue
				}
			}
		}

		if err := tx.Commit(); err != nil {
			slog.Error("error COMMIT", err)
			time.Sleep(errorSleep)
			continue
		}

		for _, yetSentRideStatus := range yetSentRideStatuses {
			data, err := json.Marshal(&chairGetNotificationResponseData{
				RideID: ride.ID,
				User: simpleUser{
					ID:   user.ID,
					Name: fmt.Sprintf("%s %s", user.Firstname, user.Lastname),
				},
				PickupCoordinate: Coordinate{
					Latitude:  ride.PickupLatitude,
					Longitude: ride.PickupLongitude,
				},
				DestinationCoordinate: Coordinate{
					Latitude:  ride.DestinationLatitude,
					Longitude: ride.DestinationLongitude,
				},
				Status: yetSentRideStatus.Status,
			})

			if err != nil {
				slog.Error("error UPDATE ride_statuses", err)
				time.Sleep(errorSleep)
				continue
			}

			printAndFlush(w, fmt.Sprintf("data: %s\n\n", data))
		}
	}
}

type postChairRidesRideIDStatusRequest struct {
	Status string `json:"status"`
}

func chairPostRideStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rideID := r.PathValue("ride_id")

	chair := ctx.Value("chair").(*Chair)

	req := &postChairRidesRideIDStatusRequest{}
	if err := bindJSON(r, req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	tx, err := db.Beginx()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()

	ride := &Ride{}
	if err := tx.GetContext(ctx, ride, "SELECT * FROM rides WHERE id = ? FOR UPDATE", rideID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, errors.New("ride not found"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if ride.ChairID.String != chair.ID {
		writeError(w, http.StatusBadRequest, errors.New("not assigned to this ride"))
		return
	}

	switch req.Status {
	// Acknowledge the ride
	case "ENROUTE":
		if _, err := tx.ExecContext(ctx, "INSERT INTO ride_statuses (id, ride_id, status) VALUES (?, ?, ?)", ulid.Make().String(), ride.ID, "ENROUTE"); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	// After Picking up user
	case "CARRYING":
		status, err := getLatestRideStatus(ctx, tx, ride.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if status != "PICKUP" {
			writeError(w, http.StatusBadRequest, errors.New("chair has not arrived yet"))
			return
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO ride_statuses (id, ride_id, status) VALUES (?, ?, ?)", ulid.Make().String(), ride.ID, "CARRYING"); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	default:
		writeError(w, http.StatusBadRequest, errors.New("invalid status"))
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
