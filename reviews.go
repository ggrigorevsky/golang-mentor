// Отзывы со Stepik.
//
// Stepik отдаёт отзывы публично через https://stepik.org/api/course-reviews,
// но без CORS-заголовков — напрямую из браузера их не забрать. Поэтому сервер
// сайта ходит в Stepik сам, склеивает отзывы с именами и аватарами авторов,
// отбрасывает пустые и низкие оценки и держит результат в памяти.
//
// Фронтенд обращается к своему же домену:
//
//	GET /api/reviews?course=golang
//	GET /api/reviews?course=system-design
//
// Новый отзыв на Stepik появляется на сайте сам — не позже чем через
// reviewsCacheTTL.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// reviewCourses — какие курсы разрешено запрашивать: слаг из URL → id на Stepik.
var reviewCourses = map[string]int64{
	"golang":        294637,
	"system-design": 292577,
}

const (
	reviewsMinScore  = 4                // отзывы с оценкой ниже на лендинг не идут
	reviewsMaxItems  = 12               // сколько последних отзывов отдаём фронтенду
	reviewsCacheTTL  = 15 * time.Minute // как часто ходим в Stepik
	reviewsPageSize  = 50               // размер страницы в API Stepik
	reviewsMaxPages  = 3                // предохранитель от бесконечной пагинации
	reviewsHTTPLimit = 10 * time.Second // таймаут одного запроса к Stepik
)

// stepikAPI вынесен в переменную, чтобы тесты могли подменить его на httptest-сервер.
var stepikAPI = "https://stepik.org/api"

// ── Ответ нашего API ──────────────────────────────────────────────────────────

// review — отзыв в том виде, в каком его рисует виджет на сайте.
type review struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Avatar string `json:"avatar,omitempty"`
	Score  int    `json:"score"`
	Text   string `json:"text"`
	Reply  string `json:"reply,omitempty"` // ответ автора курса, если он есть
	Date   string `json:"date"`            // RFC3339
	URL    string `json:"url"`             // ссылка на страницу отзывов курса
}

type reviewsResponse struct {
	OK        bool     `json:"ok"`
	CourseID  int64    `json:"course_id"`
	CourseURL string   `json:"course_url"`
	Total     int      `json:"total"`   // всего отзывов с оценкой на Stepik
	Average   float64  `json:"average"` // средняя оценка по всем отзывам
	Reviews   []review `json:"reviews"` // последние reviewsMaxItems подходящих
	UpdatedAt string   `json:"updated_at"`
}

// ── Структуры API Stepik ──────────────────────────────────────────────────────

type stepikReview struct {
	ID         int64     `json:"id"`
	User       int64     `json:"user"`
	Score      int       `json:"score"`
	Text       string    `json:"text"`
	ReplyText  string    `json:"reply_text"`
	CreateDate time.Time `json:"create_date"`
}

type stepikReviewsPage struct {
	Reviews []stepikReview `json:"course-reviews"`
	Meta    struct {
		Page    int  `json:"page"`
		HasNext bool `json:"has_next"`
	} `json:"meta"`
}

type stepikUser struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
	Avatar   string `json:"avatar"`
}

type stepikUsersPage struct {
	Users []stepikUser `json:"users"`
}

// ── Кэш ───────────────────────────────────────────────────────────────────────

type reviewsCacheEntry struct {
	body      []byte
	fetchedAt time.Time
}

var reviewsCache = struct {
	sync.Mutex
	entries map[string]reviewsCacheEntry
}{entries: map[string]reviewsCacheEntry{}}

// refreshMu сериализует походы в Stepik: если два посетителя открыли страницу
// одновременно, запрос уйдёт один.
var refreshMu sync.Mutex

var stepikClient = &http.Client{Timeout: reviewsHTTPLimit}

// ── HTTP-обработчик ───────────────────────────────────────────────────────────

func reviewsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.URL.Query().Get("course")
		courseID, ok := reviewCourses[slug]
		if !ok {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "неизвестный курс"})
			return
		}

		body, err := cachedReviews(r.Context(), slug, courseID)
		if err != nil {
			log.Printf("отзывы (%s): %v", slug, err)
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "Stepik недоступен"})
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=600")
		_, _ = w.Write(body)
	}
}

// cachedReviews возвращает готовый JSON из кэша, обновляя его не чаще
// reviewsCacheTTL. Если Stepik не ответил, а в кэше есть протухшие данные —
// отдаём их: лучше вчерашние отзывы, чем пустой блок на лендинге.
func cachedReviews(ctx context.Context, slug string, courseID int64) ([]byte, error) {
	reviewsCache.Lock()
	entry, ok := reviewsCache.entries[slug]
	reviewsCache.Unlock()
	if ok && time.Since(entry.fetchedAt) < reviewsCacheTTL {
		return entry.body, nil
	}

	refreshMu.Lock()
	defer refreshMu.Unlock()

	// Пока ждали мьютекс, кэш мог обновить кто-то другой.
	reviewsCache.Lock()
	entry, ok = reviewsCache.entries[slug]
	reviewsCache.Unlock()
	if ok && time.Since(entry.fetchedAt) < reviewsCacheTTL {
		return entry.body, nil
	}

	body, err := buildReviews(ctx, courseID)
	if err != nil {
		if ok {
			log.Printf("отзывы (%s): отдаю кэш от %s, ошибка обновления: %v",
				slug, entry.fetchedAt.Format(time.RFC3339), err)
			return entry.body, nil
		}
		return nil, err
	}

	reviewsCache.Lock()
	reviewsCache.entries[slug] = reviewsCacheEntry{body: body, fetchedAt: time.Now()}
	reviewsCache.Unlock()
	return body, nil
}

// buildReviews собирает готовый JSON: отзывы + авторы.
func buildReviews(ctx context.Context, courseID int64) ([]byte, error) {
	raw, err := fetchStepikReviews(ctx, courseID)
	if err != nil {
		return nil, err
	}

	// Средняя оценка считается по всем отзывам, а не только по показанным.
	total, sum := 0, 0
	for _, rv := range raw {
		if rv.Score > 0 {
			total++
			sum += rv.Score
		}
	}

	picked := make([]stepikReview, 0, reviewsMaxItems)
	for _, rv := range raw {
		if rv.Score < reviewsMinScore || strings.TrimSpace(rv.Text) == "" {
			continue
		}
		picked = append(picked, rv)
		if len(picked) == reviewsMaxItems {
			break
		}
	}

	ids := make([]int64, 0, len(picked))
	for _, rv := range picked {
		ids = append(ids, rv.User)
	}
	users, err := fetchStepikUsers(ctx, ids)
	if err != nil {
		// Без имён отзывы всё равно можно показать — подпишем «Ученик Stepik».
		log.Printf("отзывы: не удалось получить авторов: %v", err)
		users = map[int64]stepikUser{}
	}

	courseURL := fmt.Sprintf("https://stepik.org/course/%d/reviews", courseID)
	out := reviewsResponse{
		OK:        true,
		CourseID:  courseID,
		CourseURL: courseURL,
		Total:     total,
		Reviews:   make([]review, 0, len(picked)),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if total > 0 {
		out.Average = float64(sum) / float64(total)
	}

	for _, rv := range picked {
		name := "Ученик Stepik"
		avatar := ""
		if u, ok := users[rv.User]; ok {
			if n := strings.TrimSpace(u.FullName); n != "" {
				name = n
			}
			avatar = u.Avatar
		}
		out.Reviews = append(out.Reviews, review{
			ID:     rv.ID,
			Name:   name,
			Avatar: avatar,
			Score:  rv.Score,
			Text:   strings.TrimSpace(rv.Text),
			Reply:  strings.TrimSpace(rv.ReplyText),
			Date:   rv.CreateDate.Format(time.RFC3339),
			URL:    courseURL,
		})
	}

	return json.Marshal(out)
}

// fetchStepikReviews выкачивает отзывы курса, свежие сначала.
func fetchStepikReviews(ctx context.Context, courseID int64) ([]stepikReview, error) {
	var all []stepikReview

	for page := 1; page <= reviewsMaxPages; page++ {
		q := url.Values{}
		q.Set("course", strconv.FormatInt(courseID, 10))
		q.Set("page", strconv.Itoa(page))
		q.Set("page_size", strconv.Itoa(reviewsPageSize))
		q.Set("order", "-create_date")

		var got stepikReviewsPage
		if err := getJSON(ctx, stepikAPI+"/course-reviews?"+q.Encode(), &got); err != nil {
			return nil, fmt.Errorf("страница %d: %w", page, err)
		}
		all = append(all, got.Reviews...)
		if !got.Meta.HasNext || len(got.Reviews) == 0 {
			break
		}
	}

	// Не полагаемся на порядок из API: сортируем сами.
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].CreateDate.After(all[j].CreateDate)
	})
	return all, nil
}

// fetchStepikUsers подтягивает имена и аватары авторов отзывов.
func fetchStepikUsers(ctx context.Context, ids []int64) (map[int64]stepikUser, error) {
	out := map[int64]stepikUser{}
	if len(ids) == 0 {
		return out, nil
	}

	q := url.Values{}
	seen := map[int64]bool{}
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		q.Add("ids[]", strconv.FormatInt(id, 10))
	}

	var got stepikUsersPage
	if err := getJSON(ctx, stepikAPI+"/users?"+q.Encode(), &got); err != nil {
		return out, err
	}
	for _, u := range got.Users {
		out[u.ID] = u
	}
	return out, nil
}

func getJSON(ctx context.Context, rawURL string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "golang-mentor-site/1.0 (+https://github.com/golang-mentor)")

	resp, err := stepikClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stepik вернул %s", resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("разбор ответа: %w", err)
	}
	return nil
}
