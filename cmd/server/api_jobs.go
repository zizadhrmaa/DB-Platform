package main

import (
	"net/http"
	"strconv"

	"github.com/zizadhrmaa/DB-Platform/internal/auth"
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

func jobIDs(r *http.Request) (int64, int64, error) {
	dbID, err := httpx.PathID(r, "dbID")
	if err != nil {
		return 0, 0, err
	}
	id, err := httpx.PathID(r, "jobID")
	return dbID, id, err
}

// apiResumeJob: POST /api/databases/{dbID}/jobs/{jobID}/resume (juga /retry)
// Job batch partial dilanjutkan dari checkpoint; job partially_reverted
// melanjutkan pembatalannya. Tidak perlu approval ulang, tetapi worker tetap
// memeriksa ulang hak akses pengaju sebelum menjalankan.
func (app *App) apiResumeJob(w http.ResponseWriter, r *http.Request) {
	dbID, id, err := jobIDs(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	j, err := app.Jobs.GetInDB(r.Context(), dbID, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	action, from := queue.ActionRun, queue.StatusPartial
	if j.Status == queue.StatusPartiallyReverted {
		action, from = queue.ActionRevert, queue.StatusPartiallyReverted
	}
	j, err = app.Jobs.Reopen(r.Context(), dbID, id, []queue.Status{from}, action)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, j)
}

type revertJobRequest struct {
	RequestID int64 `json:"request_id"`
}

// apiRevertJob: POST /api/databases/{dbID}/jobs/{jobID}/revert {"request_id": N}
// Membatalkan batch committed job partial memakai request rollback yang
// sudah disetujui semua leader.
func (app *App) apiRevertJob(w http.ResponseWriter, r *http.Request) {
	dbID, id, err := jobIDs(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req revertJobRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.RequestID <= 0 {
		httpx.Fail(w, r, httpx.Invalid("request_id wajib diisi dengan request rollback yang sudah disetujui."))
		return
	}
	u, _ := auth.CurrentUser(r.Context())
	if _, err := app.Approvals.ConsumeRevert(r.Context(), u, dbID, id, req.RequestID); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	j, err := app.Jobs.Reopen(r.Context(), dbID, id, []queue.Status{queue.StatusPartial, queue.StatusPartiallyReverted}, queue.ActionRevert)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, j)
}
