package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

//辅助函数：尝试连接一个地址，返回是否存活
func isAlive(u *url.URL)bool{

	conn,err:=net.DialTimeout("tcp",u.Host,2*time.Second)//conn = 建立好的连接对象
	/*“先判错，再干活。”
	在 Go 语言里，只要函数返回了 error，第一件事永远是检查 if err != nil。
	在确定没有错误之前，绝对不要去碰其他的返回值（比如这里的 conn），因为它们很可能是空的。*/
	if err!=nil{
		return false	//不通
	}
	//在健康检查里，我们只利用它的 “建立连接” 功能来探测死活

	// 如果能走到这，说明成功了 (err == nil)
    // 必须在这里关闭连接，防止资源泄露
	conn.Close()//用完必须关

	return true //通
}

type Config struct{
	Port 	 string 	`json:"port"`
	Backends []string	`json:"backends"`
}

type Backend struct{
	URL 	*url.URL
	Alive 	bool
	ReverseProxy *httputil.ReverseProxy	//存好代理对象，不用每次 new
}

// 读取配置文件
func LoadConfig(filename string) (*Config, error) {
    // 1. 第一步：读取文件内容
    data,err:=os.ReadFile(filename)
    // 如果读文件报错，直接返回 error
	if err!=nil{
		return nil,err
	}
    
    // 2. 第二步：解析 JSON
    // 注意：这里要传指针 &config，不然填不进去！
    var config Config
	err=json.Unmarshal(data,&config)
    // 如果解析报错，直接返回 error
	if err!=nil{
		//fmt.Println("error:",err)这样的话只会直接打印错误，并不会抛出
		return nil,err
	}
    
    // 3. 成功！返回配置文件的指针
    return &config, nil
}

func main(){

	// 1.用下面函数解析出 端口号 后端地址
	config,err:=LoadConfig("config.json")//记得加上双引号
	if err!=nil{
		panic(err)
	}

	// 1.1定义一个切片，用来存放所有的后端对象
	var nodes []*Backend
	// 1.2遍历配置文件里的 IP 列表
	for _,ip:=range config.Backends{

		// A. 解析 URL
		u,err:=url.Parse(ip)
		if err!=nil{
			fmt.Printf("解析 URL 失败: %s\n", ip)
            continue // 跳过这个错误的 IP
		}

		// B. 创建反向代理 (Proxy)
		proxy:=httputil.NewSingleHostReverseProxy(u)

		// C. 设置 Director
		proxy.Director=func(req *http.Request){
			req.Host=u.Host
			req.URL.Host=u.Host
			req.URL.Scheme=u.Scheme
		}

		// D. 设置 ErrorHandler (错误处理)
		proxy.ErrorHandler=func(w http.ResponseWriter, r *http.Request, err error) {
			fmt.Printf(" [LB错误] 转发失败: %v\n", err)
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte("服务器挂了，正在抢救..."))
		}

		// E. 组装成 Backend 对象
		node:=&Backend{
			URL: u,
			Alive: true,
			ReverseProxy: proxy,
		}

		// F. 放入列表
		nodes=append(nodes, node)
	}

	// 启动健康检查协程
	go func(){
		for {// 无限循环
			for _,node:=range nodes{
				// 检查死活
				alive:=isAlive(node.URL)

				// 关键：只有状态变了才打印日志 & 更新状态
                // 如果以前是活的(true)，现在死了(false) -> 报错
                // 如果以前是死的(false)，现在活了(true) -> 庆祝
				if node.Alive!=alive{
					node.Alive=alive
					if alive{
						fmt.Printf(" [健康检查] %s 复活了! \n", node.URL.Host)
					}else {
						fmt.Printf(" [健康检查] %s 挂了! \n", node.URL.Host)
					}
				}
			}

            // 检查完一轮后，要休息多久？
			time.Sleep(2*time.Second)
		}
	}()
	
	r:=gin.Default()

	// 2.全局计数器
	var requestCounter uint64=0

	// 3.创建路由
	r.Any("/*path", func(c *gin.Context) {
        // 拦截 Favicon 
        if c.Request.URL.Path == "/favicon.ico" {
            c.AbortWithStatus(204)
            return
        }

        // 定义一个变量，用来装最终选中的那个“活”节点
        var targetNode *Backend = nil
    
        for i := 0; i < len(nodes); i++ {
            
            // 轮询算法 (要在循环里算!)
            current:=atomic.AddUint64(&requestCounter,1)
			index:=current%uint64(len(nodes))

            // 取出候选人
            candidate:=nodes[index]

            // 如果 candidate.Alive 是 true：
            // 1. 把它赋值给 targetNode
            // 2. 打印日志
            // 3. break 
			if candidate.Alive==true{
				targetNode=candidate
				fmt.Printf("请求 #%d -> 转发给: %s\n",current,candidate.URL.Host)
				break
			}
            
        }

        // 说明所有节点都挂了！
        if targetNode == nil {
            // 返回 502 错误
			c.String(502,"服务器全军覆没")
            return
        }

        //正式启动
		targetNode.ReverseProxy.ServeHTTP(c.Writer,c.Request)
        
    })

	r.Run(config.Port)
}
