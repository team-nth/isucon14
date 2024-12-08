package main

import (
	"math"
	"net/http"

	"github.com/samber/lo"
	"golang.org/x/sync/singleflight"
)

var group singleflight.Group

var chairMap = map[string]int{
	"リラックスシート NEO":      2,
	"エアシェル ライト":         2,
	"チェアエース S":          2,
	"スピンフレーム 01":        2,
	"ベーシックスツール プラス":     2,
	"SitEase":           2,
	"ComfortBasic":      2,
	"EasySit":           2,
	"LiteLine":          2,
	"リラックス座":            2,
	"エルゴクレスト II":        3,
	"フォームライン RX":        3,
	"シェルシート ハイブリッド":     3,
	"リカーブチェア スマート":      3,
	"フレックスコンフォート PRO":   3,
	"ErgoFlex":          3,
	"BalancePro":        3,
	"StyleSit":          3,
	"風雅（ふうが）チェア":        3,
	"AeroSeat":          3,
	"ゲーミングシート NEXUS":    3,
	"プレイスタイル Z":         3,
	"ストリームギア S1":        3,
	"クエストチェア Lite":      3,
	"エアフロー EZ":          3,
	"アルティマシート X":        5,
	"ゼンバランス EX":         5,
	"プレミアムエアチェア ZETA":   5,
	"モーションチェア RISE":     5,
	"インペリアルクラフト LUXE":   5,
	"LuxeThrone":        5,
	"ZenComfort":        5,
	"Infinity Seat":     5,
	"雅楽座":               5,
	"Titanium Line":     5,
	"プロゲーマーエッジ X1":      5,
	"スリムライン GX":         5,
	"フューチャーチェア CORE":    5,
	"シャドウバースト M":        5,
	"ステルスシート ROGUE":     5,
	"ナイトシート ブラックエディション": 7,
	"フューチャーステップ VISION": 7,
	"匠座 PRO LIMITED":    7,
	"ルミナスエアクラウン":        7,
	"エコシート リジェネレイト":     7,
	"ShadowEdition":     7,
	"Phoenix Ultra":     7,
	"匠座（たくみざ）プレミアム":     7,
	"Aurora Glow":       7,
	"Legacy Chair":      7,
	"インフィニティ GEAR V":    7,
	"ゼノバース ALPHA":       7,
	"タイタンフレーム ULTRA":    7,
	"ヴァーチェア SUPREME":    7,
	"オブシディアン PRIME":     7,
}

// このAPIをインスタンス内から一定間隔で叩かせることで、椅子とライドをマッチングさせる
func internalGetMatching(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// l0: 前のライドのpickupに到着するまでの距離
	// l1: 前のライドのdestinationに到着するまでの距離
	// l2: 前のライドのdestinationから今回のpickupまでの距離
	// l3: 今回のpickupからdestinationまでの距離
	// s: 椅子の速度

	// e1. 割り当てまでの時間: 30 - 待ち時間 (ここの30はmatchingの間隔次第で安全をとっても良い)
	// e2. 最終的な付くまでの時間: (l0 + l1 + l2 + l3) / s
	// e3. のるまでの時間: (l0 + l1 + l2) / s
	// e4. のってからつくまでの時間: l3 / s
	// 1-4を足し合わせて一番小さなやつを採用 (状況に応じて各要素は重みをつければ調整できる)

	// いったん開いている椅子からやる なので、e3,e4だけ計算するようにしてみる. l0, l1はいったん考えない(配送途中はまず入れないので)
	// できたら次に今配送中のやつも判定に入れる

	_, err, _ := group.Do("internalGetMatching", func() (interface{}, error) {
		var waitingRides []Ride
		if rows, err := db.Queryx("SELECT * FROM rides WHERE chair_id IS NULL"); err == nil {
			var ride Ride
			for rows.Next() {
				if err := rows.StructScan(&ride); err != nil {
					return nil, err
				}
				waitingRides = append(waitingRides, ride)
			}
		} else {
			return nil, err
		}
		if len(waitingRides) == 0 {
			return nil, nil
		}

		var openChairs []Chair
		if rows, err := db.Queryx(`SELECT * FROM chairs WHERE is_active = TRUE AND is_completed = TRUE`); err == nil {
			var chair Chair
			for rows.Next() {
				if err := rows.StructScan(&chair); err != nil {
					return nil, err
				}
				openChairs = append(openChairs, chair)
			}
		}

		//SELECT DISTINCT c.id, c.owner_id, c.name, c.model, c.is_active, c.access_token, c.created_at, c.updated_at, c.total_distance FROM chairs c JOIN rides r ON c.id = r.chair_id JOIN (SELECT rs.ride_id, rs.status FROM ride_statuses rs WHERE rs.status = 'Complete' AND rs.created_at = (SELECT MAX(sub_rs.created_at) FROM ride_statuses sub_rs WHERE sub_rs.ride_id = rs.ride_id)) latest_status ON r.id = latest_status.ride_id WHERE c.is_active = true;
		//WITH ranked_ride_statuses AS (SELECT rs.*, ROW_NUMBER() OVER (PARTITION BY rs.ride_id ORDER BY rs.created_at DESC) AS rnk FROM ride_statuses rs) SELECT model, latitude, longitude, c.id FROM chairs c LEFT JOIN rides r ON c.id = r.chair_id LEFT JOIN ranked_ride_statuses rs ON r.id = rs.ride_id WHERE (rs.id IS NULL OR (rs.rnk = 1 AND rs.status = 'COMPLETED')) AND c.is_active = TRUE

		for _, waitingRide := range waitingRides {
			minCost := math.MaxFloat64
			var targetChair *Chair = nil
			for _, openChair := range openChairs {
				s := chairMap[openChair.Model]
				l2 := calculateDistance(int(openChair.Latitude.Int32), int(openChair.Longitude.Int32), waitingRide.PickupLatitude, waitingRide.PickupLongitude)
				l3 := calculateDistance(waitingRide.PickupLatitude, waitingRide.PickupLongitude, waitingRide.DestinationLatitude, waitingRide.DestinationLongitude)

				e3 := float64(l2) / float64(s)
				e4 := float64(l3) / float64(s)

				cost := e3 + e4
				if minCost > cost {
					minCost = cost
					targetChair = &openChair
				}
			}

			if targetChair != nil {
				// 配車可能なので配車
				if _, err := db.ExecContext(ctx, "UPDATE rides SET chair_id = ? WHERE id = ?", targetChair.ID, waitingRide.ID); err != nil {
					return nil, err
				}
				if _, err := db.ExecContext(ctx, "UPDATE chairs SET is_completed = FALSE WHERE id = ?", targetChair.ID); err != nil {
					return nil, err
				}
				// 対象から外す
				openChairs = lo.Reject(openChairs, func(chair Chair, _ int) bool { return chair.ID == targetChair.ID })
			}
		}

		return nil, nil
	})

	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)

	// // MEMO: 一旦最も待たせているリクエストに適当な空いている椅子マッチさせる実装とする。おそらくもっといい方法があるはず…
	// ride := &Ride{}
	// if err := db.GetContext(ctx, ride, `SELECT * FROM rides WHERE chair_id IS NULL ORDER BY created_at LIMIT 1`); err != nil {
	// 	if errors.Is(err, sql.ErrNoRows) {
	// 		w.WriteHeader(http.StatusNoContent)
	// 		return
	// 	}
	// 	writeError(w, http.StatusInternalServerError, err)
	// 	return
	// }

	// matched := &Chair{}
	// empty := false
	// for i := 0; i < 10; i++ {
	// 	if err := db.GetContext(ctx, matched, "SELECT * FROM chairs INNER JOIN (SELECT id FROM chairs WHERE is_active = TRUE ORDER BY RAND() LIMIT 1) AS tmp ON chairs.id = tmp.id LIMIT 1"); err != nil {
	// 		if errors.Is(err, sql.ErrNoRows) {
	// 			w.WriteHeader(http.StatusNoContent)
	// 			return
	// 		}
	// 		writeError(w, http.StatusInternalServerError, err)
	// 	}

	// 	if err := db.GetContext(ctx, &empty, "SELECT COUNT(*) = 0 FROM (SELECT COUNT(chair_sent_at) = 6 AS completed FROM ride_statuses WHERE ride_id IN (SELECT id FROM rides WHERE chair_id = ?) GROUP BY ride_id) is_completed WHERE completed = FALSE", matched.ID); err != nil {
	// 		writeError(w, http.StatusInternalServerError, err)
	// 		return
	// 	}
	// 	if empty {
	// 		break
	// 	}
	// }
	// if !empty {
	// 	w.WriteHeader(http.StatusNoContent)
	// 	return
	// }

	// if _, err := db.ExecContext(ctx, "UPDATE rides SET chair_id = ? WHERE id = ?", matched.ID, ride.ID); err != nil {
	// 	writeError(w, http.StatusInternalServerError, err)
	// 	return
	// }

	// w.WriteHeader(http.StatusNoContent)
}
