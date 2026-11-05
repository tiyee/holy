// Package stores 管理数据库连接资源及其生命周期，可独立于 ORM 使用。
// OpenMySQL 返回由调用方负责关闭的 Store，ORM 仅借用其中的连接。
// OpenMySQLWithConfig 支持启动时配置连接池；DefaultMySQLPoolConfig 提供兼容默认值。
package stores
