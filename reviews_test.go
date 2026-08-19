package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeStepik поднимает заглушку API Stepik и подменяет адрес на время теста.
func fakeStepik(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	old := stepikAPI
	stepikAPI = srv.URL
	t.Cleanup(func() {
		stepikAPI = old
		srv.Close()
	})
}

func TestBuildReviewsFiltersAndSorts(t *testing.T) {
	fakeStepik(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/course-reviews"):
			_, _ = w.Write([]byte(`{"meta":{"page":1,"has_next":false},"course-reviews":[
				{"id":1,"user":10,"score":5,"text":"старый, но хороший","reply_text":"","create_date":"2026-01-01T00:00:00Z"},
				{"id":2,"user":20,"score":3,"text":"так себе","reply_text":"","create_date":"2026-02-01T00:00:00Z"},
				{"id":3,"user":30,"score":5,"text":"   ","reply_text":"","create_date":"2026-03-01T00:00:00Z"},
				{"id":4,"user":40,"score":4,"text":"свежий отзыв","reply_text":"спасибо!","create_date":"2026-04-01T00:00:00Z"}
			]}`))
		case strings.HasPrefix(r.URL.Path, "/users"):
			_, _ = w.Write([]byte(`{"users":[
				{"id":10,"full_name":"Иван Иванов","avatar":"https://cdn/10.svg"},
				{"id":40,"full_name":"Пётр Петров","avatar":""}
			]}`))
		default:
			http.NotFound(w, r)
		}
	})

	raw, err := buildReviews(context.Background(), 292577)
	if err != nil {
		t.Fatalf("buildReviews: %v", err)
	}

	var got reviewsResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("разбор ответа: %v", err)
	}

	if len(got.Reviews) != 2 {
		t.Fatalf("ожидали 2 отзыва (отсеиваются оценка 3 и пустой текст), получили %d", len(got.Reviews))
	}
	if got.Reviews[0].ID != 4 {
		t.Errorf("первым должен идти самый свежий отзыв (id=4), а идёт id=%d", got.Reviews[0].ID)
	}
	if got.Reviews[0].Name != "Пётр Петров" {
		t.Errorf("имя автора не подтянулось: %q", got.Reviews[0].Name)
	}
	if got.Reviews[0].Reply != "спасибо!" {
		t.Errorf("ответ автора потерян: %q", got.Reviews[0].Reply)
	}
	if got.Reviews[1].Name != "Иван Иванов" || got.Reviews[1].Avatar != "https://cdn/10.svg" {
		t.Errorf("автор старого отзыва: %+v", got.Reviews[1])
	}
	// Средняя считается по всем отзывам с оценкой: (5+3+5+4)/4 = 4.25
	if got.Total != 4 || got.Average < 4.24 || got.Average > 4.26 {
		t.Errorf("total=%d average=%v, ожидали 4 и 4.25", got.Total, got.Average)
	}
	if got.CourseURL != "https://stepik.org/course/292577/reviews" {
		t.Errorf("ссылка на курс: %q", got.CourseURL)
	}
}

func TestReviewsHandlerRejectsUnknownCourse(t *testing.T) {
	rec := httptest.NewRecorder()
	reviewsHandler()(rec, httptest.NewRequest(http.MethodGet, "/api/reviews?course=python", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ожидали 400, получили %d", rec.Code)
	}
}

func TestCachedReviewsServesStaleOnError(t *testing.T) {
	calls := 0
	fakeStepik(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls > 1 && strings.HasPrefix(r.URL.Path, "/course-reviews") {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/course-reviews") {
			_, _ = w.Write([]byte(`{"meta":{"page":1,"has_next":false},"course-reviews":[
				{"id":1,"user":10,"score":5,"text":"отлично","reply_text":"","create_date":"2026-01-01T00:00:00Z"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"users":[{"id":10,"full_name":"Иван","avatar":""}]}`))
	})

	first, err := cachedReviews(context.Background(), "test", 1)
	if err != nil {
		t.Fatalf("первый запрос: %v", err)
	}

	// Протухаем кэш вручную и ломаем Stepik: должен вернуться старый ответ.
	reviewsCache.Lock()
	e := reviewsCache.entries["test"]
	e.fetchedAt = time.Now().Add(-2 * reviewsCacheTTL)
	reviewsCache.entries["test"] = e
	reviewsCache.Unlock()

	second, err := cachedReviews(context.Background(), "test", 1)
	if err != nil {
		t.Fatalf("второй запрос не должен падать: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("ожидали тот же (устаревший) ответ из кэша")
	}
}
