// Package repository contains the GORM-backed runtime persistence layer.
//
// Database schema ownership remains in internal/db/migrations. Runtime code
// must not call AutoMigrate: production upgrades require reviewed, versioned,
// forward-only SQL migrations.
package repository
