//go:build production

package notes

// appDataDirectory 与 backend/storage 的构建命名空间保持一致：开发构建与正式构建的数据互不干扰。
const appDataDirectory = "com.tangoui.mincode"
