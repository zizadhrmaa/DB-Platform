package main

import (
	"net/http"
	"strconv"

	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
	"github.com/zizadhrmaa/DB-Platform/internal/queue"
)

// apiListJobs: GET /api/databases/{dbID}/jobs?status=&limit=
// Mengembalikan job database ini, terbaru dulu.
func (app *App) apiListJobs(w http.ResponseWriter, r *http.Request) {
	dbID, err := httpx.PathID(r, "dbID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := queue.ListFilter{Status: queue.Status(r.URL.Query().Get("status"))}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			httpx.Fail(w, r, httpx.Invalid("limit harus bilangan bulat positif."))
			return
		}
		f.Limit = n
	}
	jobs, err := app.Jobs.List(r.Context(), dbID, f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, jobs)
}

// apiGetJob: GET /api/databases/{dbID}/jobs/{jobID}
// Job dari database lain dijawab 404 supaya ID tidak bisa ditebak lewat URL.
func (app *App) apiGetJob(w http.ResponseWriter, r *http.Request) {
	dbID, err := httpx.PathID(r, "dbID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	jobID, err := httpx.PathID(r, "jobID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	j, err := app.Jobs.GetInDB(r.Context(), dbID, jobID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, j)
}
