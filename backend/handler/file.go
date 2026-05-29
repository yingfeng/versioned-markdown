package handler

import (
	"llmwiki/backend/service"

	"github.com/gin-gonic/gin"
)

type FileHandler struct {
	svc *service.FileService
}

func NewFileHandler(svc *service.FileService) *FileHandler {
	return &FileHandler{svc: svc}
}

func (h *FileHandler) ListFiles(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	parentID := c.Query("parent_id")
	page := parseInt(c.DefaultQuery("page", "1"), 1)
	pageSize := parseInt(c.DefaultQuery("page_size", "20"), 20)
	keywords := c.Query("keywords")

	files, total, err := h.svc.ListFiles(tenantID, parentID, page, pageSize, keywords)
	if err != nil {
		ginAbort(c, 500, "list files: "+err.Error())
		return
	}
	ginJSON(c, gin.H{
		"data":  files,
		"total": total,
		"page":  page,
	})
}

func (h *FileHandler) UploadFile(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	parentID := c.PostForm("parent_id")
	createdBy := c.GetString("user_id")

	form, err := c.MultipartForm()
	if err != nil {
		ginAbort(c, 400, "multipart form required")
		return
	}

	files := form.File["file"]
	if len(files) == 0 {
		ginAbort(c, 400, "no files uploaded")
		return
	}

	results, err := h.svc.UploadFile(tenantID, parentID, createdBy, files)
	if err != nil {
		ginAbort(c, 500, "upload: "+err.Error())
		return
	}
	ginJSON(c, gin.H{"data": results})
}

func (h *FileHandler) DeleteFiles(c *gin.Context) {
	var req struct {
		FileIDs []string `json:"file_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		ginAbort(c, 400, "bad request: "+err.Error())
		return
	}
	if err := h.svc.DeleteFiles(req.FileIDs); err != nil {
		ginAbort(c, 500, "delete: "+err.Error())
		return
	}
	ginJSON(c, gin.H{"ok": true})
}

func (h *FileHandler) MoveFiles(c *gin.Context) {
	var req struct {
		FileIDs  []string          `json:"file_ids"`
		DestID   string            `json:"dest_parent_id"`
		NewNames map[string]string `json:"new_names,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		ginAbort(c, 400, "bad request: "+err.Error())
		return
	}
	if err := h.svc.MoveFiles(req.FileIDs, req.DestID, req.NewNames); err != nil {
		ginAbort(c, 500, "move: "+err.Error())
		return
	}
	ginJSON(c, gin.H{"ok": true})
}

func (h *FileHandler) GetFile(c *gin.Context) {
	fileID := c.Param("id")
	f, data, err := h.svc.GetFileContent(fileID)
	if err != nil {
		ginAbort(c, 404, "file not found: "+err.Error())
		return
	}
	c.Writer.Header().Set("Content-Disposition", "attachment; filename="+f.Name)
	c.Writer.Header().Set("Content-Type", "application/octet-stream")
	c.Writer.Write(data)
}

func (h *FileHandler) GetFileAncestors(c *gin.Context) {
	fileID := c.Param("id")
	tenantID := c.GetString("tenant_id")
	chain, err := h.svc.GetAllParentFolders(fileID, tenantID)
	if err != nil {
		ginAbort(c, 404, err.Error())
		return
	}
	ginJSON(c, gin.H{"data": chain})
}

// GetWorkspaceChanges GET /api/v1/workspaces/:id/changes
func (h *FileHandler) GetWorkspaceChanges(c *gin.Context) {
	folderID := c.Param("id")
	changes, err := h.svc.GetUncommittedChanges(folderID)
	if err != nil {
		ginAbort(c, 500, "get changes: "+err.Error())
		return
	}
	if changes == nil {
		changes = []service.UncommittedChange{}
	}
	ginJSON(c, gin.H{"data": changes})
}

// DeleteWorkspace DELETE /api/v1/workspaces/:folder_id
func (h *FileHandler) DeleteWorkspace(c *gin.Context) {
	folderID := c.Param("id")
	if err := h.svc.HardDeleteWorkspace(folderID); err != nil {
		ginAbort(c, 500, "delete workspace: "+err.Error())
		return
	}
	ginJSON(c, gin.H{"ok": true})
}

// CreateTextFile POST /api/v1/files/text
func (h *FileHandler) CreateTextFile(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	createdBy := c.GetString("user_id")

	var req struct {
		ParentID string `json:"parent_id"`
		Name     string `json:"name"`
		Content  string `json:"content"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		ginAbort(c, 400, "name and content required")
		return
	}

	f, err := h.svc.CreateTextFile(tenantID, req.ParentID, req.Name, req.Content, createdBy)
	if err != nil {
		ginAbort(c, 500, "create file: "+err.Error())
		return
	}
	ginJSON(c, gin.H{"data": f})
}

// GetFolderTree GET /api/v1/folders/:id/tree
func (h *FileHandler) GetFolderTree(c *gin.Context) {
	id := c.Param("id")
	// If id looks like a dataset ID, get tenant tree
	s, err := h.svc.GetFileByID(id)
	treeID := id
	if err != nil {
		// Not a file ID - try as dataset ID to get root folder tree
		t, err := h.svc.GetDatasetTree(id)
		if err == nil && t != nil {
			ginJSON(c, gin.H{"data": t})
			return
		}
		ginAbort(c, 404, "tree not found")
		return
	}
	if s != nil && s.Type == "folder" {
		treeID = s.ID
	}
	tree, err := h.svc.GetCurrentTree(treeID)
	if err != nil {
		ginAbort(c, 404, "tree not found: "+err.Error())
		return
	}
	ginJSON(c, gin.H{"data": tree})
}

func (h *FileHandler) CreateFolder(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	createdBy := c.GetString("user_id")

	var req struct {
		ParentID string `json:"parent_id"`
		Name     string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Name == "" {
		ginAbort(c, 400, "name required")
		return
	}

	f, err := h.svc.CreateFolder(tenantID, req.ParentID, req.Name, createdBy)
	if err != nil {
		ginAbort(c, 500, "create folder: "+err.Error())
		return
	}
	ginJSON(c, gin.H{"data": f})
}
