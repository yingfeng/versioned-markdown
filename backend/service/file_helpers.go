package service

import (
	"encoding/json"
	"fmt"
	"llmwiki/backend/entity"
	"path"
	"strings"
)

// GetFileByID returns a file by its ID.
func (s *FileService) GetFileByID(fileID string) (*entity.File, error) {
	return s.fileDAO.GetByID(fileID)
}

// GetAllParentFolders returns all ancestor folders for a file.
func (s *FileService) GetAllParentFolders(fileID, tenantID string) ([]entity.File, error) {
	return s.fileDAO.GetAllParentFolders(fileID, tenantID)
}

// GetStorageData retrieves raw data from MinIO for the given bucket/key.
func (s *FileService) GetStorageData(bucket, key string) ([]byte, error) {
	return s.storageImpl.Get(bucket, key)
}

// GetDatasetIDByFileID returns the KB ID associated with a file.
func (s *FileService) GetDatasetIDByFileID(fileID string) (string, error) {
	return s.fileDAO.GetDatasetIDByFileID(fileID)
}

// UncommittedChange describes a file change not yet committed.
type UncommittedChange struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	Operation string `json:"operation"` // "add", "modify", "delete"
}

type fileInfo struct {
	Name     string
	Location *string
}

// GetUncommittedChanges returns files that differ from the last commit.
func (s *FileService) GetUncommittedChanges(folderID string) ([]UncommittedChange, error) {
	// Get current live file tree
	live := s.buildTreeState(folderID, nil)
	if live == nil {
		return nil, fmt.Errorf("folder not found")
	}

	// Collect current files (id -> name, location)
	currentMap := make(map[string]fileInfo)
	var walk func(*entity.TreeNode)
	walk = func(n *entity.TreeNode) {
		for _, child := range n.Children {
			if child.Type == "file" {
				currentMap[child.ID] = fileInfo{Name: child.Name, Location: child.Location}
			} else {
				walk(&child)
			}
		}
	}
	walk(live)

	// Get last commit's tree state
	lastCommit, err := s.commitDAO.GetLatestCommit(folderID)
	if err != nil || lastCommit == nil || lastCommit.TreeState == nil {
		var changes []UncommittedChange
		for id, info := range currentMap {
			changes = append(changes, UncommittedChange{FileID: id, FileName: info.Name, Operation: "add"})
		}
		return changes, nil
	}

	// Parse last commit tree
	var lastTree entity.TreeNode
	if err := json.Unmarshal([]byte(*lastCommit.TreeState), &lastTree); err != nil {
		return nil, err
	}

	// Collect last commit files (id -> name, location)
	lastMap := make(map[string]fileInfo)
	walk = func(n *entity.TreeNode) {
		for _, child := range n.Children {
			if child.Type == "file" {
				lastMap[child.ID] = fileInfo{Name: child.Name, Location: child.Location}
			} else {
				walk(&child)
			}
		}
	}
	walk(&lastTree)

	// Diff by comparing locations
	var changes []UncommittedChange
	for id, cur := range currentMap {
		last, exists := lastMap[id]
		if !exists {
			changes = append(changes, UncommittedChange{FileID: id, FileName: cur.Name, Operation: "add"})
		} else {
			// Compare locations to detect modification
			curLoc := ""
			if cur.Location != nil { curLoc = *cur.Location }
			lastLoc := ""
			if last.Location != nil { lastLoc = *last.Location }
			if curLoc != lastLoc {
				changes = append(changes, UncommittedChange{FileID: id, FileName: cur.Name, Operation: "modify"})
			}
		}
	}
	for id, info := range lastMap {
		if _, exists := currentMap[id]; !exists {
			changes = append(changes, UncommittedChange{FileID: id, FileName: info.Name, Operation: "delete"})
		}
	}

	return changes, nil
}

// hardDeleteWorkspace cascading deletes a workspace folder and all its contents.
func (s *FileService) HardDeleteWorkspace(folderID string) error {
	// Collect all file IDs recursively
	var allIDs []string
	var collect func(pid string)
	collect = func(pid string) {
		children, _, _ := s.fileDAO.GetByParentID(pid, 1, 10000, "")
		for _, c := range children {
			allIDs = append(allIDs, c.ID)
			if c.Type == "folder" {
				collect(c.ID)
			}
			// Remove from storage
			if c.Location != nil && *c.Location != "" {
				_ = s.storageImpl.Remove(folderID, *c.Location)
			}
		}
	}
	collect(folderID)
	allIDs = append(allIDs, folderID)

	// Delete commits and items for this folder
	_ = s.commitDAO.DeleteByFolderID(folderID)

	// Delete file2document links
	for _, id := range allIDs {
		_ = s.f2dDAO.DeleteByFileID(id)
	}

	// Delete file records
	return s.fileDAO.DeleteByIDs(allIDs)
}

// GetDatasetTree returns the root folder tree for a dataset.
func (s *FileService) GetDatasetTree(kbID string) (*entity.TreeNode, error) {
	kb, err := s.kbDAO.GetByID(kbID)
	if err != nil {
		return nil, err
	}
	root, err := s.fileDAO.GetRootFolder(kb.TenantID)
	if err != nil {
		// Auto-create root folder if it doesn't exist
		root, err = s.fileDAO.CreateRootFolder(kb.TenantID, kb.CreatedBy)
		if err != nil {
			return nil, err
		}
	}
	return s.GetCurrentTree(root.ID)
}

// CreateTextFile creates a new text file with the given content under a parent folder.
func (s *FileService) CreateTextFile(tenantID, parentID, name, content, createdBy string) (*entity.File, error) {
	if parentID == "" {
		root, err := s.GetOrCreateRootFolder(tenantID, createdBy)
		if err != nil {
			return nil, err
		}
		parentID = root.ID
	}

	data := []byte(content)
	contentHash := sha256Hex(data)
	ext := path.Ext(name)
	location := fmt.Sprintf("%s/%s%s", parentID, contentHash[:16], ext)

	if err := s.storageImpl.Put(parentID, location, data); err != nil {
		return nil, fmt.Errorf("storage put %s: %w", name, err)
	}

	fileType := strings.TrimPrefix(ext, ".")
	if fileType == "" {
		fileType = "other"
	}

	fileRec := &entity.File{
		ID:         entity.NewID(),
		ParentID:   parentID,
		TenantID:   tenantID,
		CreatedBy:  createdBy,
		Name:       name,
		Location:   &location,
		Size:       int64(len(data)),
		Type:       fileType,
		SourceType: "local",
	}
	if err := s.fileDAO.Create(fileRec); err != nil {
		return nil, fmt.Errorf("create file record: %w", err)
	}
	return fileRec, nil
}
