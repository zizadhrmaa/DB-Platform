// Package admin mengelola user, group, anggota group, dan grant akses database.

// Catatan:
//   - Route /admin/* sudah dibungkus authz.RequireAdmin.
//   - User dinonaktifkan, dihapus, atau password direset: panggil
//     SessionManager.RevokeAllForUser.
//   - Admin aktif terakhir tidak boleh dinonaktifkan atau dihapus. Cek di
//     dalam transaction.
//   - Grant pakai INSERT ... ON CONFLICT (group_id, db_id) DO UPDATE.
//   - Cabut akses dengan DELETE barisnya, bukan menyimpan level "none".
package admin
