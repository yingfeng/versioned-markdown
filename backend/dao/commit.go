package dao

import (
	"llmwiki/backend/entity"

)

type CommitDAO struct{}

func NewCommitDAO() *CommitDAO { return &CommitDAO{} }

func (d *CommitDAO) CreateCommit(c *entity.FileCommit) error {
	if c.ID == "" {
		c.ID = entity.NewID()
	}
	return DB.Create(c).Error
}

func (d *CommitDAO) GetCommitByID(id string) (*entity.FileCommit, error) {
	var c entity.FileCommit
	err := DB.Where("id = ?", id).First(&c).Error
	return &c, err
}

func (d *CommitDAO) ListCommits(folderID string, page, pageSize int) ([]entity.FileCommit, int64, error) {
	var commits []entity.FileCommit
	var total int64
	query := DB.Where("folder_id = ?", folderID)
	query.Model(&entity.FileCommit{}).Count(&total)
	err := query.Offset((page - 1) * pageSize).Limit(pageSize).Order("create_time DESC").Find(&commits).Error
	return commits, total, err
}

func (d *CommitDAO) GetLatestCommit(folderID string) (*entity.FileCommit, error) {
	var c entity.FileCommit
	err := DB.Where("folder_id = ?", folderID).Order("create_time DESC").First(&c).Error
	return &c, err
}

func (d *CommitDAO) CreateItem(item *entity.FileCommitItem) error {
	if item.ID == "" {
		item.ID = entity.NewID()
	}
	return DB.Create(item).Error
}

func (d *CommitDAO) GetItemsByCommitID(commitID string) ([]entity.FileCommitItem, error) {
	var items []entity.FileCommitItem
	err := DB.Where("commit_id = ?", commitID).Find(&items).Error
	return items, err
}

func (d *CommitDAO) GetItemsByCommitIDs(commitIDs []string) ([]entity.FileCommitItem, error) {
	var items []entity.FileCommitItem
	err := DB.Where("commit_id IN ?", commitIDs).Find(&items).Error
	return items, err
}

func (d *CommitDAO) GetItemsByFileID(fileID string) ([]entity.FileCommitItem, error) {
	var items []entity.FileCommitItem
	err := DB.Where("file_id = ?", fileID).Order("create_time DESC").Find(&items).Error
	return items, err
}
