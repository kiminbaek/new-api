package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	NameRuleExact = iota
	NameRulePrefix
	NameRuleContains
	NameRuleSuffix
)

type BoundChannel struct {
	Name string `json:"name"`
	Type int    `json:"type"`
}

type Model struct {
	Id           int            `json:"id"`
	ModelName    string         `json:"model_name" gorm:"size:128;not null;uniqueIndex:uk_model_name_delete_at,priority:1"`
	Description  string         `json:"description,omitempty" gorm:"type:text"`
	Icon         string         `json:"icon,omitempty" gorm:"type:varchar(128)"`
	Tags         string         `json:"tags,omitempty" gorm:"type:varchar(255)"`
	VendorID     int            `json:"vendor_id,omitempty" gorm:"index"`
	Endpoints    string         `json:"endpoints,omitempty" gorm:"type:text"`
	Status       int            `json:"status" gorm:"default:1"`
	SyncOfficial int            `json:"sync_official" gorm:"default:1"`
	CreatedTime  int64          `json:"created_time" gorm:"bigint"`
	UpdatedTime  int64          `json:"updated_time" gorm:"bigint"`
	Revision     int64          `json:"revision" gorm:"not null;default:1"`
	DeletedAt    gorm.DeletedAt `json:"-" gorm:"index;uniqueIndex:uk_model_name_delete_at,priority:2"`

	BoundChannels []BoundChannel `json:"bound_channels,omitempty" gorm:"-"`
	EnableGroups  []string       `json:"enable_groups,omitempty" gorm:"-"`
	QuotaTypes    []int          `json:"quota_types,omitempty" gorm:"-"`
	NameRule      int            `json:"name_rule" gorm:"default:0"`

	MatchedModels []string `json:"matched_models,omitempty" gorm:"-"`
	MatchedCount  int      `json:"matched_count,omitempty" gorm:"-"`
}

func (mi *Model) Insert() error {
	now := common.GetTimestamp()
	mi.CreatedTime = now
	mi.UpdatedTime = now

	// 保存原始值（因为 Create 后可能被 GORM 的 default 标签覆盖为 1）
	originalStatus := mi.Status
	originalSyncOfficial := mi.SyncOfficial

	// 先创建记录（GORM 会对零值字段应用默认值）
	if err := DB.Create(mi).Error; err != nil {
		return err
	}

	// 使用保存的原始值进行更新，确保零值能正确保存
	return DB.Model(&Model{}).Where("id = ?", mi.Id).Updates(map[string]interface{}{
		"status":        originalStatus,
		"sync_official": originalSyncOfficial,
	}).Error
}

func IsModelNameDuplicated(id int, name string) (bool, error) {
	if name == "" {
		return false, nil
	}
	var cnt int64
	err := DB.Model(&Model{}).Where("model_name = ? AND id <> ?", name, id).Count(&cnt).Error
	return cnt > 0, err
}

func (mi *Model) Update() error {
	mi.UpdatedTime = common.GetTimestamp()
	// 使用 Select 强制更新所有字段，包括零值
	return DB.Model(&Model{}).Where("id = ?", mi.Id).
		Select("model_name", "description", "icon", "tags", "vendor_id", "endpoints", "status", "sync_official", "name_rule", "updated_time").
		Updates(mi).Error
}

var ErrModelRevisionConflict = errors.New("model revision conflict")

var modelPricingAfterModelWriteHook func() error

var modelPricingOptionKeys = []string{
	"ModelPrice", "ModelRatio", "CacheRatio", "CompletionRatio",
	"ImageRatio", "AudioRatio", "AudioCompletionRatio",
}

type ModelPricingPatch struct {
	ModelPrice           *float64 `json:"model_price"`
	ModelRatio           *float64 `json:"model_ratio"`
	CacheRatio           *float64 `json:"cache_ratio"`
	CompletionRatio      *float64 `json:"completion_ratio"`
	ImageRatio           *float64 `json:"image_ratio"`
	AudioRatio           *float64 `json:"audio_ratio"`
	AudioCompletionRatio *float64 `json:"audio_completion_ratio"`
}

func (p ModelPricingPatch) values() map[string]*float64 {
	return map[string]*float64{
		"ModelPrice": p.ModelPrice, "ModelRatio": p.ModelRatio,
		"CacheRatio": p.CacheRatio, "CompletionRatio": p.CompletionRatio,
		"ImageRatio": p.ImageRatio, "AudioRatio": p.AudioRatio,
		"AudioCompletionRatio": p.AudioCompletionRatio,
	}
}

func ensureModelPricingOptionRows() error {
	for _, key := range modelPricingOptionKeys {
		option := Option{Key: key, Value: "{}"}
		if err := DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoNothing: true}).Create(&option).Error; err != nil {
			return err
		}
	}
	return nil
}

func readPricingMapTx(tx *gorm.DB, key string) (map[string]float64, error) {
	var option Option
	result := lockForUpdate(tx).Where(commonKeyCol+" = ?", key).Limit(1).Find(&option)
	if result.Error != nil {
		return nil, result.Error
	}
	values := make(map[string]float64)
	if result.RowsAffected == 0 || strings.TrimSpace(option.Value) == "" {
		return values, nil
	}
	if err := json.Unmarshal([]byte(option.Value), &values); err != nil {
		return nil, err
	}
	return values, nil
}

func writePricingMapTx(tx *gorm.DB, key string, values map[string]float64) error {
	encoded, err := json.Marshal(values)
	if err != nil {
		return err
	}
	option := Option{Key: key, Value: string(encoded)}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&option).Error
}

// publishModelPricingOptions updates the seven pricing option snapshots as one
// in-memory generation. The database transaction has already validated and
// committed every JSON value, so publishing cannot leave a partially refreshed
// pricing cache when one per-key hot-loader fails midway.
func publishModelPricingOptions(updates map[string]string) error {
	if len(updates) != len(modelPricingOptionKeys) {
		return errors.New("incomplete model pricing option snapshot")
	}
	for _, key := range modelPricingOptionKeys {
		value, ok := updates[key]
		if !ok {
			return fmt.Errorf("missing model pricing option %s", key)
		}
		var decoded map[string]float64
		if err := json.Unmarshal([]byte(value), &decoded); err != nil {
			return fmt.Errorf("invalid committed model pricing option %s: %w", key, err)
		}
	}
	common.OptionMapRWMutex.Lock()
	for _, key := range modelPricingOptionKeys {
		common.OptionMap[key] = updates[key]
	}
	common.OptionMapRWMutex.Unlock()
	RefreshPricing()
	return nil
}

func applyModelPricingPatchTx(tx *gorm.DB, oldName string, newName string, patch ModelPricingPatch) (map[string]string, error) {
	updates := make(map[string]string, len(modelPricingOptionKeys))
	patchValues := patch.values()
	keys := append([]string(nil), modelPricingOptionKeys...)
	sort.Strings(keys)
	for _, key := range keys {
		values, err := readPricingMapTx(tx, key)
		if err != nil {
			return nil, err
		}
		if oldName != "" && oldName != newName {
			delete(values, oldName)
		}
		value := patchValues[key]
		delete(values, newName)
		if value != nil {
			values[newName] = *value
		}
		if err := writePricingMapTx(tx, key, values); err != nil {
			return nil, err
		}
		encoded, _ := json.Marshal(values)
		updates[key] = string(encoded)
	}
	return updates, nil
}

func CreateModelWithPricing(incoming *Model, patch ModelPricingPatch) error {
	if incoming == nil || strings.TrimSpace(incoming.ModelName) == "" {
		return errors.New("invalid model atomic create")
	}
	optionBulkUpdateMu.Lock()
	defer optionBulkUpdateMu.Unlock()
	if err := ensureModelPricingOptionRows(); err != nil {
		return err
	}
	var optionUpdates map[string]string
	err := DB.Transaction(func(tx *gorm.DB) error {
		var duplicate int64
		if err := tx.Model(&Model{}).Where("model_name = ?", incoming.ModelName).Count(&duplicate).Error; err != nil {
			return err
		}
		if duplicate > 0 {
			return errors.New("model name already exists")
		}
		now := common.GetTimestamp()
		incoming.CreatedTime = now
		incoming.UpdatedTime = now
		incoming.Revision = 1
		originalStatus := incoming.Status
		originalSyncOfficial := incoming.SyncOfficial
		if err := tx.Create(incoming).Error; err != nil {
			return err
		}
		if err := tx.Model(&Model{}).Where("id = ?", incoming.Id).Updates(map[string]any{
			"status": originalStatus, "sync_official": originalSyncOfficial,
		}).Error; err != nil {
			return err
		}
		if modelPricingAfterModelWriteHook != nil {
			if err := modelPricingAfterModelWriteHook(); err != nil {
				return err
			}
		}
		var err error
		optionUpdates, err = applyModelPricingPatchTx(tx, "", incoming.ModelName, patch)
		return err
	})
	if err != nil {
		return err
	}
	return publishModelPricingOptions(optionUpdates)
}

// SaveModelWithPricing atomically updates one model entity and its seven
// per-model pricing entries. expectedRevision provides optimistic concurrency;
// unrelated model entries are merged under row locks instead of overwritten.
func SaveModelWithPricing(incoming *Model, expectedRevision int64, patch ModelPricingPatch) error {
	if incoming == nil || incoming.Id <= 0 || expectedRevision <= 0 {
		return errors.New("invalid model atomic update")
	}
	optionBulkUpdateMu.Lock()
	defer optionBulkUpdateMu.Unlock()
	if err := ensureModelPricingOptionRows(); err != nil {
		return err
	}
	var optionUpdates map[string]string
	err := DB.Transaction(func(tx *gorm.DB) error {
		var current Model
		if err := lockForUpdate(tx).Where("id = ?", incoming.Id).First(&current).Error; err != nil {
			return err
		}
		if current.Revision != expectedRevision {
			return ErrModelRevisionConflict
		}
		if incoming.ModelName == "" {
			return errors.New("model name is empty")
		}
		var duplicate int64
		if err := tx.Model(&Model{}).Where("model_name = ? AND id <> ?", incoming.ModelName, incoming.Id).Count(&duplicate).Error; err != nil {
			return err
		}
		if duplicate > 0 {
			return errors.New("model name already exists")
		}
		now := common.GetTimestamp()
		result := tx.Model(&Model{}).Where("id = ? AND revision = ?", incoming.Id, expectedRevision).
			Updates(map[string]any{
				"model_name": incoming.ModelName, "description": incoming.Description,
				"icon": incoming.Icon, "tags": incoming.Tags, "vendor_id": incoming.VendorID,
				"endpoints": incoming.Endpoints, "status": incoming.Status,
				"sync_official": incoming.SyncOfficial, "name_rule": incoming.NameRule,
				"updated_time": now, "revision": gorm.Expr("revision + 1"),
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrModelRevisionConflict
		}
		if modelPricingAfterModelWriteHook != nil {
			if err := modelPricingAfterModelWriteHook(); err != nil {
				return err
			}
		}
		var err error
		optionUpdates, err = applyModelPricingPatchTx(tx, current.ModelName, incoming.ModelName, patch)
		return err
	})
	if err != nil {
		return err
	}
	if err := publishModelPricingOptions(optionUpdates); err != nil {
		return err
	}
	incoming.Revision = expectedRevision + 1
	incoming.UpdatedTime = common.GetTimestamp()
	return nil
}

func (mi *Model) Delete() error {
	return DB.Delete(mi).Error
}

func GetVendorModelCounts() (map[int64]int64, error) {
	var stats []struct {
		VendorID int64
		Count    int64
	}
	if err := DB.Model(&Model{}).
		Select("vendor_id as vendor_id, count(*) as count").
		Group("vendor_id").
		Scan(&stats).Error; err != nil {
		return nil, err
	}
	m := make(map[int64]int64, len(stats))
	for _, s := range stats {
		m[s.VendorID] = s.Count
	}
	return m, nil
}

func GetAllModels(offset int, limit int) ([]*Model, error) {
	models, _, err := SearchModels("", "", "", "", offset, limit)
	return models, err
}

func GetBoundChannelsByModelsMap(modelNames []string) (map[string][]BoundChannel, error) {
	result := make(map[string][]BoundChannel)
	if len(modelNames) == 0 {
		return result, nil
	}
	type row struct {
		Model string
		Name  string
		Type  int
	}
	var rows []row
	err := DB.Table("channels").
		Select("abilities.model as model, channels.name as name, channels.type as type").
		Joins("JOIN abilities ON abilities.channel_id = channels.id").
		Where("abilities.model IN ? AND abilities.enabled = ?", modelNames, true).
		Distinct().
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		result[r.Model] = append(result[r.Model], BoundChannel{Name: r.Name, Type: r.Type})
	}
	return result, nil
}

func normalizeLookupValues(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized
}

func GetPreferredModelOwnerChannelTypes(modelNames []string, groups []string) (map[string]int, error) {
	result := make(map[string]int)
	modelNames = normalizeLookupValues(modelNames)
	if len(modelNames) == 0 {
		return result, nil
	}

	type row struct {
		Model       string
		ChannelType int
	}
	var rows []row

	query := DB.Table("abilities").
		Select("abilities.model as model, channels.type as channel_type").
		Joins("JOIN channels ON abilities.channel_id = channels.id").
		Where("abilities.model IN ? AND abilities.enabled = ? AND channels.status = ?", modelNames, true, common.ChannelStatusEnabled).
		Order("COALESCE(abilities.priority, 0) DESC").
		Order("abilities.weight DESC").
		Order("abilities.channel_id ASC")

	groups = normalizeLookupValues(groups)
	if len(groups) > 0 {
		query = query.Where("abilities."+commonGroupCol+" IN ?", groups)
	}

	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}

	for _, r := range rows {
		if _, ok := result[r.Model]; ok {
			continue
		}
		result[r.Model] = r.ChannelType
	}
	return result, nil
}

func SearchModels(keyword string, vendor string, status string, syncOfficial string, offset int, limit int) ([]*Model, int64, error) {
	var models []*Model
	db := DB.Model(&Model{})
	if keyword != "" {
		like := "%" + keyword + "%"
		db = db.Where("model_name LIKE ? OR description LIKE ? OR tags LIKE ?", like, like, like)
	}
	if vendor != "" {
		if vid, err := strconv.Atoi(vendor); err == nil {
			db = db.Where("models.vendor_id = ?", vid)
		} else {
			db = db.Joins("JOIN vendors ON vendors.id = models.vendor_id").Where("vendors.name LIKE ?", "%"+vendor+"%")
		}
	}
	if statusValue, ok := parseModelStatusFilter(status); ok {
		db = db.Where("models.status = ?", statusValue)
	}
	if syncValue, ok := parseModelSyncFilter(syncOfficial); ok {
		db = db.Where("models.sync_official = ?", syncValue)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if err := db.Order("models.id DESC").Offset(offset).Limit(limit).Find(&models).Error; err != nil {
		return nil, 0, err
	}
	return models, total, nil
}

// parseModelStatusFilter maps UI/API status values to the models.status column.
// Returns ok=false when no status filter should be applied.
func parseModelStatusFilter(status string) (value int, ok bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "all":
		return 0, false
	case "enabled", "1":
		return 1, true
	case "disabled", "0":
		return 0, true
	default:
		n, err := strconv.Atoi(status)
		if err != nil {
			return 0, false
		}
		return n, true
	}
}

// parseModelSyncFilter maps UI/API sync values to the models.sync_official column.
// Returns ok=false when no sync filter should be applied.
func parseModelSyncFilter(syncOfficial string) (value int, ok bool) {
	switch strings.ToLower(strings.TrimSpace(syncOfficial)) {
	case "", "all":
		return 0, false
	case "yes", "1":
		return 1, true
	case "no", "0":
		return 0, true
	default:
		n, err := strconv.Atoi(syncOfficial)
		if err != nil {
			return 0, false
		}
		return n, true
	}
}
