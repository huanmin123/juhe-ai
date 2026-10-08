// 本文件是 jobs internal/accounthealth/config.go 的成对常量副本（成对关系见
// probe.go 包注释）：仅下沉探针执行器闭包引用的容量上限。jobs 留守侧保留
// 同名常量原实现（J1 reader/config 仍在使用），两侧取值必须一致。
package exactkeyprobe

// maxJ1Capacity 是探针并发的机制容量上限，与 jobs accounthealth config.go 的
// maxJ1Capacity（5096）同源同值；修改任一侧必须同步另一侧。
const maxJ1Capacity = 5096
