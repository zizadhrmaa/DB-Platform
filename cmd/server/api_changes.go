package main

import (
	"net/http"

	"github.com/zizadhrmaa/DB-Platform/internal/approval"
	"github.com/zizadhrmaa/DB-Platform/internal/auth"
	"github.com/zizadhrmaa/DB-Platform/internal/httpx"
	"github.com/zizadhrmaa/DB-Platform/internal/queue"
)

type createChangeRequest struct {
	Kind              queue.Kind `json:"kind"`
	TargetExecutionID *int64     `json:"target_execution_id"`
}

type rejectChangeRequest struct {
	Reason string `json:"reason"`
}

func changeIDs(r *http.Request) (int64, int64, error) {
	dbID, err := httpx.PathID(r, "dbID")
	if err != nil {
		return 0, 0, err
	}
	id, err := httpx.PathID(r, "requestID")
	return dbID, id, err
}

func (app *App) apiCreateChange(w http.ResponseWriter, r *http.Request) {
	dbID, err := httpx.PathID(r, "dbID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	migrationID, err := httpx.PathID(r, "migrationID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req createChangeRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	u, _ := auth.CurrentUser(r.Context())
	d, err := app.Approvals.Create(r.Context(), u, dbID, migrationID, req.Kind, req.TargetExecutionID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, d)
}

func (app *App) apiListChanges(w http.ResponseWriter, r *http.Request) {
	dbID, err := httpx.PathID(r, "dbID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	list, err := app.Approvals.List(r.Context(), dbID, approval.Status(r.URL.Query().Get("status")))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, list)
}

func (app *App) apiGetChange(w http.ResponseWriter, r *http.Request) {
	dbID, id, err := changeIDs(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	d, err := app.Approvals.Get(r.Context(), dbID, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

func (app *App) apiSubmitChange(w http.ResponseWriter, r *http.Request) {
	dbID, id, err := changeIDs(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	d, err := app.Approvals.Submit(r.Context(), dbID, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

func (app *App) apiApproveChange(w http.ResponseWriter, r *http.Request) {
	dbID, id, err := changeIDs(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	u, _ := auth.CurrentUser(r.Context())
	d, err := app.Approvals.Approve(r.Context(), u, dbID, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

func (app *App) apiRejectChange(w http.ResponseWriter, r *http.Request) {
	dbID, id, err := changeIDs(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req rejectChangeRequest
	if err := httpx.ReadJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	u, _ := auth.CurrentUser(r.Context())
	d, err := app.Approvals.Reject(r.Context(), u, dbID, id, req.Reason)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

func (app *App) apiEnqueueChange(w http.ResponseWriter, r *http.Request) {
	dbID, id, err := changeIDs(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	u, _ := auth.CurrentUser(r.Context())
	job, err := app.Approvals.Enqueue(r.Context(), u, dbID, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID, "job_status": job.Status})
}

func (app *App) apiCancelChange(w http.ResponseWriter, r *http.Request) {
	dbID, id, err := changeIDs(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	u, _ := auth.CurrentUser(r.Context())
	d, err := app.Approvals.Cancel(r.Context(), u, dbID, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}
