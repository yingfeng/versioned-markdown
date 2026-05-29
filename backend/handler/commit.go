package handler

import (
	"llmwiki/backend/entity"
	"llmwiki/backend/service"

	"github.com/gin-gonic/gin"
)

type CommitHandler struct {
	fileSvc *service.FileService
}

func NewCommitHandler(fileSvc *service.FileService) *CommitHandler {
	return &CommitHandler{fileSvc: fileSvc}
}

// CreateCommit POST /api/v1/workspaces/:folder_id/commits
func (h *CommitHandler) CreateCommit(c *gin.Context) {
	folderID := c.Param("folder_id")
	authorID := c.GetString("user_id")

	var req struct {
		Message string                   `json:"message"`
		Files   []service.CommitFileInput `json:"files"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		ginAbort(c, 400, "bad request: "+err.Error())
		return
	}
	if len(req.Files) == 0 {
		ginAbort(c, 400, "at least one file required")
		return
	}

	commit, err := h.fileSvc.CreateCommit(folderID, authorID, req.Message, req.Files)
	if err != nil {
		ginAbort(c, 500, "create commit: "+err.Error())
		return
	}
	ginJSON(c, gin.H{"data": commit})
}

// ListCommits GET /api/v1/workspaces/:folder_id/commits
func (h *CommitHandler) ListCommits(c *gin.Context) {
	folderID := c.Param("folder_id")
	page := parseInt(c.DefaultQuery("page", "1"), 1)
	pageSize := parseInt(c.DefaultQuery("page_size", "20"), 20)

	commits, total, err := h.fileSvc.ListCommits(folderID, page, pageSize)
	if err != nil {
		ginAbort(c, 500, "list commits: "+err.Error())
		return
	}
	ginJSON(c, gin.H{"data": commits, "total": total, "page": page})
}

// GetCommit GET /api/v1/datasets/:dataset_id/commits/:commit_id
func (h *CommitHandler) GetCommit(c *gin.Context) {
	commitID := c.Param("commit_id")
	commit, err := h.fileSvc.GetCommit(commitID)
	if err != nil {
		ginAbort(c, 404, "commit not found")
		return
	}
	ginJSON(c, gin.H{"data": commit})
}

// ListCommitFiles GET /api/v1/datasets/:dataset_id/commits/:commit_id/files
func (h *CommitHandler) ListCommitFiles(c *gin.Context) {
	commitID := c.Param("commit_id")
	items, err := h.fileSvc.ListCommitFiles(commitID)
	if err != nil {
		ginAbort(c, 500, "list commit files: "+err.Error())
		return
	}

	// Enrich with file names
	type FileChange struct {
		entity.FileCommitItem
		FileName string `json:"file_name"`
	}
	var result []FileChange
	for _, item := range items {
		fc := FileChange{FileCommitItem: item}
		f, err := h.fileSvc.GetFileByID(item.FileID)
		if err == nil {
			fc.FileName = f.Name
		}
		result = append(result, fc)
	}

	ginJSON(c, gin.H{"data": result})
}

// DiffCommits GET /api/v1/datasets/:dataset_id/commits/diff?from=:from_id&to=:to_id
func (h *CommitHandler) DiffCommits(c *gin.Context) {
	fromID := c.Query("from")
	toID := c.Query("to")
	if fromID == "" || toID == "" {
		ginAbort(c, 400, "from and to query params required")
		return
	}

	diff, err := h.fileSvc.DiffCommits(fromID, toID)
	if err != nil {
		ginAbort(c, 500, "diff commits: "+err.Error())
		return
	}
	ginJSON(c, gin.H{"data": diff})
}

// ListFolderCommits is now handled by ListCommits via /workspaces/:folder_id/commits
// kept for backward compatibility
func (h *CommitHandler) ListFolderCommits(c *gin.Context) {
	folderID := c.Param("id")
	h.ListCommits(c) // reuse new handler, but param name mismatch - inline instead
	page := parseInt(c.DefaultQuery("page", "1"), 1)
	pageSize := parseInt(c.DefaultQuery("page_size", "20"), 20)
	commits, total, err := h.fileSvc.ListCommits(folderID, page, pageSize)
	if err != nil {
		ginAbort(c, 500, "list commits: "+err.Error())
		return
	}
	ginJSON(c, gin.H{"data": commits, "total": total, "page": page})
}

// GetCommitTreeHandler GET /api/v1/commits/:commit_id/tree
func (h *CommitHandler) GetCommitTreeHandler(c *gin.Context) {
	commitID := c.Param("commit_id")
	tree, err := h.fileSvc.GetCommitTree(commitID)
	if err != nil {
		ginAbort(c, 404, "commit tree not found: "+err.Error())
		return
	}
	ginJSON(c, gin.H{"data": tree})
}

// GetFileAtCommit GET /api/v1/commits/:commit_id/files/:file_id/content
func (h *CommitHandler) GetFileAtCommit(c *gin.Context) {
	commitID := c.Param("commit_id")
	fileID := c.Param("file_id")

	data, err := h.fileSvc.GetCommitFileContent(commitID, fileID)
	if err != nil {
		ginAbort(c, 404, "file content not found: "+err.Error())
		return
	}
	c.Writer.Header().Set("Content-Type", "application/octet-stream")
	c.Writer.Write(data)
}

// GetCommitFileContent GET /api/v1/datasets/:dataset_id/commits/:commit_id/files/:file_id/content
func (h *CommitHandler) GetCommitFileContent(c *gin.Context) {
	commitID := c.Param("commit_id")
	fileID := c.Param("file_id")

	// Get commit items to find the hash for this file in this commit
	items, err := h.fileSvc.ListCommitFiles(commitID)
	if err != nil {
		ginAbort(c, 500, err.Error())
		return
	}

	var targetHash *string
	for _, item := range items {
		if item.FileID == fileID {
			targetHash = item.NewHash
			break
		}
	}
	if targetHash == nil || *targetHash == "" {
		ginAbort(c, 404, "file not found in this commit")
		return
	}

	// Get KB to find bucket
	kbID := c.Param("dataset_id")
	objKey := ".objects/" + *targetHash

	data, err := h.fileSvc.GetStorageData(kbID, objKey)
	if err != nil {
		ginAbort(c, 404, "content not found")
		return
	}
	c.Writer.Header().Set("Content-Type", "application/octet-stream")
	c.Writer.Write(data)
}
