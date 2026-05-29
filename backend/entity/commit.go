package entity

// FileCommit represents a commit — a batch of file changes within a workspace folder.
type FileCommit struct {
	ID        string  `gorm:"column:id;primaryKey;size:32" json:"id"`
	FolderID  string  `gorm:"column:folder_id;size:32;not null;index" json:"folder_id"`
	ParentID  string  `gorm:"column:parent_id;size:32;index" json:"parent_id,omitempty"`
	Message   string  `gorm:"column:message;size:512" json:"message"`
	AuthorID  string  `gorm:"column:author_id;size:32;not null;index" json:"author_id"`
	FileCount int     `gorm:"column:file_count;default:0" json:"file_count"`
	TreeState *string `gorm:"column:tree_state;type:longtext" json:"tree_state,omitempty"`
	BaseModel
}

func (FileCommit) TableName() string { return "file_commit" }

// FileCommitOp represents the operation type for a commit item.
type FileCommitOp string

const (
	CommitOpAdd    FileCommitOp = "add"
	CommitOpModify FileCommitOp = "modify"
	CommitOpDelete FileCommitOp = "delete"
	CommitOpRename FileCommitOp = "rename"
)

// FileCommitItem represents an individual file change within a commit.
type FileCommitItem struct {
	ID         string       `gorm:"column:id;primaryKey;size:32" json:"id"`
	CommitID   string       `gorm:"column:commit_id;size:32;not null;index" json:"commit_id"`
	FileID     string       `gorm:"column:file_id;size:32;not null;index" json:"file_id"`
	Operation  FileCommitOp `gorm:"column:operation;size:16;not null" json:"operation"`
	OldHash    *string      `gorm:"column:old_hash;size:64;index" json:"old_hash,omitempty"`
	NewHash    *string      `gorm:"column:new_hash;size:64;index" json:"new_hash,omitempty"`
	OldLocation *string     `gorm:"column:old_location;size:255" json:"old_location,omitempty"`
	NewLocation *string     `gorm:"column:new_location;size:255" json:"new_location,omitempty"`
	OldName    *string      `gorm:"column:old_name;size:255" json:"old_name,omitempty"`
	NewName    *string      `gorm:"column:new_name;size:255" json:"new_name,omitempty"`
	BaseModel
}

func (FileCommitItem) TableName() string { return "file_commit_item" }

// TreeNode represents a node in the folder tree snapshot.
type TreeNode struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Type        string     `json:"type"` // "folder" or "file"
	Children    []TreeNode `json:"children,omitempty"`
	ContentHash *string    `json:"content_hash,omitempty"`
	Location    *string    `json:"location,omitempty"`
	Size        int64      `json:"size,omitempty"`
}
