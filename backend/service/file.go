package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"llmwiki/backend/dao"
	"llmwiki/backend/entity"
	"llmwiki/backend/storage"
	"mime/multipart"
	"path"
	"strconv"
	"strings"

)

type FileService struct {
	fileDAO         *dao.FileDAO
	f2dDAO          *dao.File2DocumentDAO
	docDAO          *dao.DocumentDAO
	kbDAO           *dao.KnowledgebaseDAO
	commitDAO       *dao.CommitDAO
	storageImpl     storage.Storage
}

func NewFileService(s storage.Storage) *FileService {
	return &FileService{
		fileDAO:     dao.NewFileDAO(),
		f2dDAO:      dao.NewFile2DocumentDAO(),
		docDAO:      dao.NewDocumentDAO(),
		kbDAO:       dao.NewKnowledgebaseDAO(),
		commitDAO:   dao.NewCommitDAO(),
		storageImpl: s,
	}
}

// GetOrCreateRootFolder returns the root folder for a tenant, creating if not exists.
func (s *FileService) GetOrCreateRootFolder(tenantID, createdBy string) (*entity.File, error) {
	f, err := s.fileDAO.GetRootFolder(tenantID)
	if err != nil {
		return s.fileDAO.CreateRootFolder(tenantID, createdBy)
	}
	return f, nil
}

// ListFiles returns paginated files under a parent folder.
func (s *FileService) ListFiles(tenantID, parentID string, page, pageSize int, keywords string) ([]entity.File, int64, error) {
	// If parentID is empty, use root folder
	if parentID == "" {
		root, err := s.GetOrCreateRootFolder(tenantID, "")
		if err != nil {
			return nil, 0, err
		}
		parentID = root.ID
	}
	return s.fileDAO.GetByParentID(parentID, page, pageSize, keywords)
}

// UploadFile uploads one or more files under a parent folder.
func (s *FileService) UploadFile(tenantID, parentID, createdBy string, files []*multipart.FileHeader) ([]*entity.File, error) {
	if parentID == "" {
		root, err := s.GetOrCreateRootFolder(tenantID, createdBy)
		if err != nil {
			return nil, err
		}
		parentID = root.ID
	}

	var results []*entity.File
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			return nil, fmt.Errorf("open file %s: %w", fh.Filename, err)
		}
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(f); err != nil {
			f.Close()
			return nil, fmt.Errorf("read file %s: %w", fh.Filename, err)
		}
		f.Close()

		data := buf.Bytes()
		contentHash := sha256Hex(data)
		ext := path.Ext(fh.Filename)
		location := fmt.Sprintf("%s/%s%s", parentID, contentHash[:16], ext)

		// Store to MinIO
		if err := s.storageImpl.Put(parentID, location, data); err != nil {
			return nil, fmt.Errorf("storage put %s: %w", fh.Filename, err)
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
			Name:       fh.Filename,
			Location:   &location,
			Size:       int64(len(data)),
			Type:       fileType,
			SourceType: "local",
		}
		if err := s.fileDAO.Create(fileRec); err != nil {
			return nil, fmt.Errorf("create file record: %w", err)
		}
		results = append(results, fileRec)
	}
	return results, nil
}

// DeleteFiles deletes files recursively.
func (s *FileService) DeleteFiles(fileIDs []string) error {
	for _, id := range fileIDs {
		if err := s.deleteSingleFile(id); err != nil {
			return err
		}
	}
	return nil
}

func (s *FileService) deleteSingleFile(fileID string) error {
	f, err := s.fileDAO.GetByID(fileID)
	if err != nil {
		return err
	}

	// Recursively delete children
	childIDs, _ := s.fileDAO.ListChildIDs(fileID)
	if len(childIDs) > 0 {
		if err := s.DeleteFiles(childIDs); err != nil {
			return err
		}
	}

	// Remove from storage if it's a file
	if f.Location != nil && *f.Location != "" {
		_ = s.storageImpl.Remove(f.ParentID, *f.Location)
	}

	// Remove file2document links
	_ = s.f2dDAO.DeleteByFileID(fileID)

	// Remove DB record
	return s.fileDAO.DeleteByID(nil, fileID)
}

// MoveFiles moves/renames files.
func (s *FileService) MoveFiles(srcFileIDs []string, destParentID string, newNames map[string]string) error {
	for _, srcID := range srcFileIDs {
		f, err := s.fileDAO.GetByID(srcID)
		if err != nil {
			return fmt.Errorf("file %s not found: %w", srcID, err)
		}

		newName := newNames[srcID]
		if newName == "" {
			newName = f.Name
		}

		updates := map[string]interface{}{
			"name": newName,
		}
		if destParentID != "" {
			updates["parent_id"] = destParentID
		}
		if err := s.fileDAO.UpdateByID(srcID, updates); err != nil {
			return fmt.Errorf("move file %s: %w", srcID, err)
		}
	}
	return nil
}

// GetFileContent retrieves file metadata and storage data.
func (s *FileService) GetFileContent(fileID string) (*entity.File, []byte, error) {
	f, err := s.fileDAO.GetByID(fileID)
	if err != nil {
		return nil, nil, err
	}
	if f.Location == nil || *f.Location == "" {
		return f, nil, nil
	}
	data, err := s.storageImpl.Get(f.ParentID, *f.Location)
	if err != nil {
		return f, nil, err
	}
	return f, data, nil
}

// CreateCommit creates a new commit for a workspace folder.
func (s *FileService) CreateCommit(folderID, authorID, message string, fileChanges []CommitFileInput) (*entity.FileCommit, error) {
	prev, _ := s.commitDAO.GetLatestCommit(folderID)
	parentID := ""
	if prev != nil {
		parentID = prev.ID
	}

	commit := &entity.FileCommit{
		ID:        entity.NewID(),
		FolderID:  folderID,
		ParentID:  parentID,
		Message:   message,
		AuthorID:  authorID,
		FileCount: len(fileChanges),
	}
	if err := s.commitDAO.CreateCommit(commit); err != nil {
		return nil, fmt.Errorf("create commit: %w", err)
	}

	// Track new hashes for tree_state generation
	commitNewHashes := make(map[string]string)

	for _, change := range fileChanges {
		item := &entity.FileCommitItem{
			CommitID:  commit.ID,
			FileID:    change.FileID,
			Operation: change.Operation,
		}

		switch change.Operation {
		case entity.CommitOpDelete:
			f, err := s.fileDAO.GetByID(change.FileID)
			if err == nil {
				item.OldLocation = f.Location
				item.OldHash = &change.ContentHash
				if f.Location != nil {
					item.OldLocation = f.Location
				}
				// Mark file as deleted
				_ = s.fileDAO.UpdateByID(change.FileID, map[string]interface{}{
					"status": "0",
				})
			}

		case entity.CommitOpAdd, entity.CommitOpModify:
			if change.Content == "" {
				continue
			}
			hash := sha256Hex([]byte(change.Content))
			contentHash := hash

			if change.Operation == entity.CommitOpAdd {
				f, err := s.fileDAO.GetByID(change.FileID)
				if err != nil {
					_ = s.fileDAO.Create(&entity.File{
						ID:   change.FileID,
						Name: change.FileName,
					})
					f, _ = s.fileDAO.GetByID(change.FileID)
				}
				if f != nil {
					item.OldLocation = f.Location
				}
			} else {
				f, err := s.fileDAO.GetByID(change.FileID)
				if err == nil {
					item.OldLocation = f.Location
					if f.Location != nil {
						item.OldLocation = f.Location
					}
				}
			}

			objKey := fmt.Sprintf(".objects/%s", contentHash)
			if err := s.storageImpl.Put(folderID, objKey, []byte(change.Content)); err != nil {
				return nil, fmt.Errorf("store object %s: %w", contentHash, err)
			}

			newLoc := &objKey
			item.NewHash = &contentHash
			item.NewLocation = newLoc
			commitNewHashes[change.FileID] = contentHash

			_ = s.fileDAO.UpdateByID(change.FileID, map[string]interface{}{
				"location": newLoc,
				"size":     int64(len(change.Content)),
			})

		case entity.CommitOpRename:
			item.OldName = &change.OldName
			item.NewName = &change.NewName
			_ = s.fileDAO.UpdateByID(change.FileID, map[string]interface{}{
				"name": change.NewName,
			})
		}

		if err := s.commitDAO.CreateItem(item); err != nil {
			return nil, fmt.Errorf("create commit item: %w", err)
		}
	}

	// Build and store tree state snapshot (workspace-scoped)
	treeRoot := s.buildTreeState(folderID, commitNewHashes)
	if treeRoot != nil {
		treeJSON, _ := json.Marshal(treeRoot)
		if treeJSON != nil {
			treeStr := string(treeJSON)
			commit.TreeState = &treeStr
			// Persist tree_state to DB
			_ = dao.DB.Model(&entity.FileCommit{}).Where("id = ?", commit.ID).Update("tree_state", treeStr)
		}
	}

	return commit, nil
}

// generateTreeStateJSON builds a JSON tree snapshot of the KB root folder.
func (s *FileService) generateTreeStateJSON(kbID string, newHashes map[string]string) *string {
	kb, err := s.kbDAO.GetByID(kbID)
	if err != nil {
		return nil
	}
	rootFolder, err := s.fileDAO.GetRootFolder(kb.TenantID)
	if err != nil {
		return nil
	}
	treeRoot := s.buildTreeState(rootFolder.ID, newHashes)
	if treeRoot == nil {
		return nil
	}
	treeJSON, err := json.Marshal(treeRoot)
	if err != nil {
		return nil
	}
	treeStr := string(treeJSON)
	return &treeStr
}

// ListCommits lists commit history for a workspace folder.
func (s *FileService) ListCommits(folderID string, page, pageSize int) ([]entity.FileCommit, int64, error) {
	return s.commitDAO.ListCommits(folderID, page, pageSize)
}

// GetCommit returns a single commit's details.
func (s *FileService) GetCommit(commitID string) (*entity.FileCommit, error) {
	return s.commitDAO.GetCommitByID(commitID)
}

// ListCommitFiles lists all file changes in a commit.
func (s *FileService) ListCommitFiles(commitID string) ([]entity.FileCommitItem, error) {
	return s.commitDAO.GetItemsByCommitID(commitID)
}

// DiffCommits returns the diff between two commits.
func (s *FileService) DiffCommits(fromID, toID string) (*CommitDiff, error) {
	fromItems, err := s.commitDAO.GetItemsByCommitID(fromID)
	if err != nil {
		return nil, fmt.Errorf("from commit %s: %w", fromID, err)
	}
	toItems, err := s.commitDAO.GetItemsByCommitID(toID)
	if err != nil {
		return nil, fmt.Errorf("to commit %s: %w", toID, err)
	}

	fromMap := make(map[string]*entity.FileCommitItem)
	for _, item := range fromItems {
		item := item
		fromMap[item.FileID] = &item
	}
	toMap := make(map[string]*entity.FileCommitItem)
	for _, item := range toItems {
		item := item
		toMap[item.FileID] = &item
	}

	diff := &CommitDiff{
		FromCommit: fromID,
		ToCommit:   toID,
	}

	allFileIDs := make(map[string]bool)
	for _, item := range fromItems {
		allFileIDs[item.FileID] = true
	}
	for _, item := range toItems {
		allFileIDs[item.FileID] = true
	}

	for fid := range allFileIDs {
		oldItem := fromMap[fid]
		newItem := toMap[fid]

		var change CommitDiffChange
		change.FileID = fid

		if oldItem == nil {
			// Added in to
			change.Operation = "added"
			if newItem != nil {
				change.NewHash = newItem.NewHash
				change.NewLocation = newItem.NewLocation
			}
		} else if newItem == nil {
			// Deleted in to (was in from but not to)
			change.Operation = "deleted"
			change.OldHash = oldItem.NewHash
			change.OldLocation = oldItem.NewLocation
		} else if (oldItem.NewHash != nil && newItem.NewHash != nil && *oldItem.NewHash != *newItem.NewHash) ||
			(oldItem.NewName != nil && *oldItem.NewName != *newItem.NewName) {
			// Modified
			change.Operation = "modified"
			change.OldHash = oldItem.NewHash
			change.OldLocation = oldItem.NewLocation
			change.NewHash = newItem.NewHash
			change.NewLocation = newItem.NewLocation
			if oldItem.NewName != nil && newItem.NewName != nil && *oldItem.NewName != *newItem.NewName {
				change.Operation = "renamed"
				change.OldName = oldItem.NewName
				change.NewName = newItem.NewName
			}
		}
		// Fetch file name for display
		f, err := s.fileDAO.GetByID(fid)
		if err == nil {
			change.FileName = f.Name
		}

		if change.Operation != "" {
			diff.Changes = append(diff.Changes, change)
		}
	}

	return diff, nil
}

// CommitFileInput describes a file change within a commit.
type CommitFileInput struct {
	FileID      string           `json:"file_id"`
	FileName    string           `json:"file_name"`
	Operation   entity.FileCommitOp `json:"operation"`
	Content     string           `json:"content"`
	ContentHash string           `json:"content_hash"`
	OldName     string           `json:"old_name,omitempty"`
	NewName     string           `json:"new_name,omitempty"`
}

// CommitDiff represents the diff result between two commits.
type CommitDiff struct {
	FromCommit string             `json:"from_commit"`
	ToCommit   string             `json:"to_commit"`
	Changes    []CommitDiffChange `json:"changes"`
}

type CommitDiffChange struct {
	FileID      string  `json:"file_id"`
	FileName    string  `json:"file_name"`
	Operation   string  `json:"operation"`
	OldHash     *string `json:"old_hash,omitempty"`
	NewHash     *string `json:"new_hash,omitempty"`
	OldLocation *string `json:"old_location,omitempty"`
	NewLocation *string `json:"new_location,omitempty"`
	OldName     *string `json:"old_name,omitempty"`
	NewName     *string `json:"new_name,omitempty"`
}

// CreateFolder creates a folder record.
func (s *FileService) CreateFolder(tenantID, parentID, name, createdBy string) (*entity.File, error) {
	if parentID == "" {
		root, err := s.GetOrCreateRootFolder(tenantID, createdBy)
		if err != nil {
			return nil, err
		}
		parentID = root.ID
	}

	f := &entity.File{
		ID:        entity.NewID(),
		ParentID:  parentID,
		TenantID:  tenantID,
		CreatedBy: createdBy,
		Name:      name,
		Type:      "folder",
	}
	if err := s.fileDAO.Create(f); err != nil {
		return nil, err
	}
	return f, nil
}

// buildTreeState recursively walks the folder tree and builds a TreeNode snapshot.
func (s *FileService) buildTreeState(folderID string, newHashes map[string]string) *entity.TreeNode {
	folder, err := s.fileDAO.GetByID(folderID)
	if err != nil {
		return nil
	}
	node := &entity.TreeNode{
		ID:   folder.ID,
		Name: folder.Name,
		Type: "folder",
	}
	children, _, _ := s.fileDAO.GetByParentID(folderID, 1, 10000, "")
	for _, child := range children {
		// Skip self-referencing root folder
		if child.ID == folderID {
			continue
		}
		// Skip deleted files (status == "0")
		if child.Status == "0" {
			continue
		}
		if child.Type == "folder" {
			childNode := s.buildTreeState(child.ID, newHashes)
			if childNode != nil {
				node.Children = append(node.Children, *childNode)
			}
		} else {
			fileNode := entity.TreeNode{
				ID:       child.ID,
				Name:     child.Name,
				Type:     "file",
				Location: child.Location,
				Size:     child.Size,
			}
			if hash, ok := newHashes[child.ID]; ok {
				h := hash
				fileNode.ContentHash = &h
			} else if child.Location != nil {
				f2dList, _ := s.f2dDAO.GetByFileID(child.ID)
				for _, f2d := range f2dList {
					if f2d.DocumentID != nil {
						doc, err := s.docDAO.GetByID(*f2d.DocumentID)
						if err == nil && doc.ContentHash != nil {
							fileNode.ContentHash = doc.ContentHash
							break
						}
					}
				}
			}
			node.Children = append(node.Children, fileNode)
		}
	}
	return node
}

// GetCommitTree returns the folder tree snapshot stored in a specific commit.
func (s *FileService) GetCommitTree(commitID string) (*entity.TreeNode, error) {
	c, err := s.commitDAO.GetCommitByID(commitID)
	if err != nil {
		return nil, fmt.Errorf("commit not found: %w", err)
	}
	if c.TreeState == nil || *c.TreeState == "" {
		return nil, fmt.Errorf("commit %s has no tree state", commitID)
	}
	var tree entity.TreeNode
	if err := json.Unmarshal([]byte(*c.TreeState), &tree); err != nil {
		return nil, fmt.Errorf("unmarshal tree state: %w", err)
	}
	return &tree, nil
}

// GetCurrentTree returns the current live folder tree for a given folder.
func (s *FileService) GetCurrentTree(folderID string) (*entity.TreeNode, error) {
	tree := s.buildTreeState(folderID, nil)
	if tree == nil {
		return nil, fmt.Errorf("folder %s not found", folderID)
	}
	return tree, nil
}

// GetCommitFileContent retrieves file content as it existed at a specific commit.
func (s *FileService) GetCommitFileContent(commitID, fileID string) ([]byte, error) {
	c, err := s.commitDAO.GetCommitByID(commitID)
	if err != nil {
		return nil, fmt.Errorf("commit not found: %w", err)
	}

	items, err := s.commitDAO.GetItemsByCommitID(commitID)
	if err != nil {
		return nil, fmt.Errorf("get commit items: %w", err)
	}

	var targetLocation *string
	for _, item := range items {
		if item.FileID == fileID {
			if item.NewLocation != nil && *item.NewLocation != "" {
				targetLocation = item.NewLocation
				break
			}
			if item.OldLocation != nil && *item.OldLocation != "" {
				targetLocation = item.OldLocation
			}
			break
		}
	}

	if targetLocation == nil {
		f, err := s.fileDAO.GetByID(fileID)
		if err != nil {
			return nil, fmt.Errorf("file %s not found: %w", fileID, err)
		}
		if f.Location == nil || *f.Location == "" {
			return nil, fmt.Errorf("file %s has no location", fileID)
		}
		targetLocation = f.Location
	}

	return s.storageImpl.Get(c.FolderID, *targetLocation)
}

// GetFileVersionHistory gets all commits that modified a specific file.
func (s *FileService) GetFileVersionHistory(fileID string) ([]entity.FileCommit, error) {
	items, err := s.commitDAO.GetItemsByFileID(fileID)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var commits []entity.FileCommit
	for _, item := range items {
		if !seen[item.CommitID] {
			seen[item.CommitID] = true
			c, err := s.commitDAO.GetCommitByID(item.CommitID)
			if err == nil {
				commits = append(commits, *c)
			}
		}
	}
	return commits, nil
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// getFilenameSuffix returns a suffix for duplicate filenames.
func getUniqueFilename(name string, existing []entity.File) string {
	for _, e := range existing {
		if e.Name == name {
			ext := path.Ext(name)
			base := strings.TrimSuffix(name, ext)
			// Try base(1).ext, base(2).ext, etc.
			for i := 1; ; i++ {
				candidate := base + "(" + strconv.Itoa(i) + ")" + ext
				dup := false
				for _, e2 := range existing {
					if e2.Name == candidate {
						dup = true
						break
					}
				}
				if !dup {
					return candidate
				}
			}
		}
	}
	return name
}
