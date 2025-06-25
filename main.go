package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

//go:embed static/*
//go:embed .env
var embeddedFiles embed.FS

// 静态文件系统包装，自动给路径加 static/ 前缀
type staticFS struct {
	fs http.FileSystem
}

func (s staticFS) Open(name string) (http.File, error) {
	if strings.HasPrefix(name, "/static") {
		name = name[1:]
	}
	return s.fs.Open(name)
}

var (
	bot       *tgbotapi.BotAPI
	chatID    int64
	accessPwd string
)

func main() {
	// 定义命令行参数（默认值为空）
	portFlag := flag.String("port", "", "服务端口")
	botTokenFlag := flag.String("bot_token", "", "Telegram Bot Token")
	accessPwdFlag := flag.String("access_pwd", "", "访问密码")
	proxyFlag := flag.String("proxy", "", "HTTP 代理地址")
	chatIDFlag := flag.String("chat_id", "", "Telegram Chat ID")
	baseURLFlag := flag.String("base_url", "", "服务的基础 URL，例如 https://yourdomain.com")
	flag.Parse()

	envLoaded := false

	// 尝试加载 .env 文件
	if _, err := os.Stat(".env"); err == nil {
		if err := godotenv.Load(".env"); err != nil {
			log.Fatal("加载外部 .env 文件失败:", err)
		}
		log.Println("使用外部 .env 配置")
		envLoaded = true
	} else {
		// 使用嵌入 .env
		envBytes, err := embeddedFiles.ReadFile(".env")
		if err != nil {
			log.Fatal("读取嵌入 .env 文件失败:", err)
		}
		envMap, err := godotenv.Parse(strings.NewReader(string(envBytes)))
		if err != nil {
			log.Fatal("解析嵌入 .env 失败:", err)
		}
		for k, v := range envMap {
			os.Setenv(k, v)
		}
		log.Println("使用嵌入的 .env 配置")
	}

	// 如果命令行指定了参数，就覆盖环境变量
	overrideEnv := func(key, value string) {
		if value != "" {
			os.Setenv(key, value)
		}
	}
	overrideEnv("PORT", *portFlag)
	overrideEnv("BOT_TOKEN", *botTokenFlag)
	overrideEnv("ACCESS_PWD", *accessPwdFlag)
	overrideEnv("PROXY", *proxyFlag)
	overrideEnv("CHAT_ID", *chatIDFlag)
	overrideEnv("BASE_URL", *baseURLFlag)

	// 读取最终环境变量
	port := os.Getenv("PORT")
	botToken := os.Getenv("BOT_TOKEN")
	accessPwd = os.Getenv("ACCESS_PWD")
	proxyStr := os.Getenv("PROXY")
	chatIDStr := os.Getenv("CHAT_ID")
	baseURL := os.Getenv("BASE_URL")

	// 检查必填
	if port == "" && !envLoaded {
		log.Fatal("未找到 .env 文件，必须通过 -port 指定服务端口")
	}
	if botToken == "" || accessPwd == "" || chatIDStr == "" {
		log.Fatal("缺少必要配置，请通过 .env 或命令行设置 bot_token、access_pwd、chat_id")
	}

	var err error
	chatID, err = strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil {
		log.Fatal("CHAT_ID 格式错误，应为数字:", err)
	}

	if proxyStr != "" {
		proxyURL, err := url.Parse(proxyStr)
		if err != nil {
			log.Fatal("代理地址格式错误:", err)
		}

		client := &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			},
		}
		bot, err = tgbotapi.NewBotAPIWithClient(botToken, tgbotapi.APIEndpoint, client)
		if err != nil {
			log.Fatal("初始化 Bot 失败:", err)
		}
		http.DefaultTransport = &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		}
	} else {
		bot, err = tgbotapi.NewBotAPI(botToken)
		if err != nil {
			log.Fatal("初始化 Bot 失败:", err)
		}
	}

	go func() {
		_, _ = bot.Send(tgbotapi.NewMessage(chatID, "🤖tg-disk服务启动成功🎉🎉\n\n"+
			"指定文件回复get获取URL链接\n\n源码地址：https://github.com/Yohann0617/tg-disk"))

		u := tgbotapi.NewUpdate(0)
		u.Timeout = 60
		updates := bot.GetUpdatesChan(u)

		for update := range updates {
			if update.Message.From.ID != chatID {
				_, _ = bot.Send(tgbotapi.NewMessage(update.Message.From.ID, "您无权限使用此机器人"))
			}

			if update.Message == nil || update.Message.ReplyToMessage == nil {
				continue
			}

			// 只处理私聊
			msgText := strings.TrimSpace(update.Message.Text)
			if update.Message.Chat.IsPrivate() && (msgText == "get" || msgText == "/get") {
				if baseURL == "" {
					msg := tgbotapi.NewMessage(update.Message.From.ID, "未配置 BASE_URL 参数，无法获取完整URL链接")
					_, _ = bot.Send(msg)
					continue
				}

				var msg *tgbotapi.Message
				if update.Message != nil {
					msg = update.Message
				}

				var fileID, fileName string
				replyToMessage := msg.ReplyToMessage

				switch {
				case replyToMessage.Document != nil && replyToMessage.Document.FileID != "":
					fileID = replyToMessage.Document.FileID
					fileName = replyToMessage.Document.FileName
				case replyToMessage.Video != nil && replyToMessage.Video.FileID != "":
					fileID = replyToMessage.Video.FileID
					fileName = replyToMessage.Video.FileName
				case replyToMessage.Audio != nil && replyToMessage.Audio.FileID != "":
					fileID = replyToMessage.Audio.FileID
					fileName = replyToMessage.Audio.FileName
				case replyToMessage.Animation != nil && replyToMessage.Animation.FileID != "":
					fileID = replyToMessage.Animation.FileID
					fileName = replyToMessage.Animation.FileName
				case replyToMessage.Sticker != nil && replyToMessage.Sticker.FileID != "":
					fileID = replyToMessage.Sticker.FileID
					fileName = replyToMessage.Sticker.Emoji
				}

				var downloadURL string
				if fileName == "fileAll.txt" {
					downloadURL = fmt.Sprintf("%s/d?file_id=%s", strings.TrimRight(baseURL, "/"), fileID)
				} else {
					downloadURL = fmt.Sprintf("%s/d?file_id=%s&filename=%s",
						strings.TrimRight(baseURL, "/"), fileID, url.QueryEscape(fileName))
				}

				var msgRsp tgbotapi.MessageConfig
				if fileID != "" {
					msgRsp = tgbotapi.NewMessage(update.Message.From.ID, "文件 ["+fileName+"] 下载链接：\n"+downloadURL)
				} else {
					msgRsp = tgbotapi.NewMessage(update.Message.From.ID, "无法获取自己上传的文件ID")
				}
				_, err := bot.Send(msgRsp)
				if err != nil {
					log.Println(err)
				}
			}
		}
	}()

	httpFS, err := fs.Sub(embeddedFiles, "static")
	if err != nil {
		log.Fatal(err)
	}
	http.Handle("/", http.FileServer(staticFS{http.FS(httpFS)}))
	http.HandleFunc("/verify", handleVerify)
	http.HandleFunc("/upload", handleUpload)
	http.HandleFunc("/d", handleDownload)

	if port == "" {
		port = "8080" // fallback
	}
	log.Printf("🎉🎉 The service is started successfully -> http://127.0.0.1:%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

type UploadResult struct {
	Filename    string `json:"filename"`
	FileID      string `json:"file_id"`
	DownloadURL string `json:"download_url"`
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "只支持 POST", http.StatusMethodNotAllowed)
		return
	}
	if r.FormValue("pwd") != accessPwd {
		http.Error(w, "密码错误", http.StatusUnauthorized)
		return
	}

	filesizeStr := r.FormValue("filesize")
	filesize, err := strconv.ParseInt(filesizeStr, 10, 64)
	if err != nil {
		filesize = 0 // 或者当做未知大小处理
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "读取文件失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	tmpDir, err := os.MkdirTemp("", "upload_")
	if err != nil {
		http.Error(w, "创建临时目录失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tmpDir)

	origFilename := header.Filename
	const chunkSize = 20 * 1024 * 1024
	var fileIDs []string
	buf := make([]byte, chunkSize)
	index := 0

	// 小于20MB直接上传
	if filesize > 0 && filesize <= chunkSize {
		tmpPath := filepath.Join(tmpDir, header.Filename)
		tmp, err := os.Create(tmpPath)
		if err != nil {
			http.Error(w, "创建临时文件失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer tmp.Close()

		_, err = io.Copy(tmp, file)
		if err != nil {
			http.Error(w, "写入临时文件失败: "+err.Error(), http.StatusInternalServerError)
			return
		}

		doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(tmpPath))
		doc.Caption = header.Filename
		msg, err := bot.Send(doc)
		if err != nil {
			log.Println("上传到 Telegram 失败: "+err.Error(), err)
			http.Error(w, "上传到 Telegram 失败: "+err.Error(), http.StatusInternalServerError)
			return
		}

		var fileId string
		switch {
		case msg.Document != nil:
			fileId = msg.Document.FileID
		case msg.Audio != nil:
			fileId = msg.Audio.FileID
		case msg.Video != nil:
			fileId = msg.Video.FileID
		case msg.Sticker != nil:
			fileId = msg.Sticker.FileID
		case msg.Animation != nil:
			fileId = msg.Animation.FileID
		}

		downloadURL := fmt.Sprintf("%s://%s/d?file_id=%s&filename=%s",
			getScheme(r), r.Host, fileId, header.Filename)

		result := UploadResult{
			Filename:    header.Filename,
			FileID:      fileId,
			DownloadURL: downloadURL,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
		return
	}

	// 大于20MB分块上传
	for {
		n, err := io.ReadFull(file, buf)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			http.Error(w, "读取文件失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if n == 0 {
			break
		}

		chunkPath := filepath.Join(tmpDir, fmt.Sprintf("blob_%d", index))
		if err := os.WriteFile(chunkPath, buf[:n], 0644); err != nil {
			http.Error(w, "写入临时分块失败: "+err.Error(), http.StatusInternalServerError)
			return
		}

		doc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(chunkPath))
		doc.Caption = "blob"
		msg, err := bot.Send(doc)
		if err != nil {
			http.Error(w, "上传分块失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if msg.Document != nil {
			fileIDs = append(fileIDs, msg.Document.FileID)
		}
		index++
		if err == io.EOF || n < chunkSize {
			break
		}
	}

	// 构建 fileAll.txt 内容
	builder := strings.Builder{}
	builder.WriteString(origFilename + "\n")
	for _, fid := range fileIDs {
		builder.WriteString(fid + "\n")
	}

	metaPath := filepath.Join(tmpDir, "fileAll.txt")
	if err := os.WriteFile(metaPath, []byte(builder.String()), 0644); err != nil {
		http.Error(w, "写入 fileAll.txt 失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// 上传 fileAll.txt
	metaDoc := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(metaPath))
	metaDoc.Caption = origFilename
	msg, err := bot.Send(metaDoc)
	if err != nil {
		http.Error(w, "上传 fileAll.txt 失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	fileID := ""
	if msg.Document != nil {
		fileID = msg.Document.FileID
	}

	downloadURL := fmt.Sprintf("%s://%s/d?file_id=%s", getScheme(r), r.Host, fileID)
	result := UploadResult{
		Filename:    origFilename,
		FileID:      fileID,
		DownloadURL: downloadURL,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	fileID := r.URL.Query().Get("file_id")
	filename := r.URL.Query().Get("filename")

	if fileID == "" {
		http.Error(w, "缺少 file_id 参数", http.StatusBadRequest)
		return
	}

	// filename 参数存在，表示是小文件，直接下载
	if filename != "" {
		tgFile, err := bot.GetFile(tgbotapi.FileConfig{FileID: fileID})
		if err != nil {
			http.Error(w, "获取文件失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", bot.Token, tgFile.FilePath)
		resp, err := http.Get(url)
		if err != nil {
			http.Error(w, "下载失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()

		ext := filepath.Ext(filename)
		contentType := mime.TypeByExtension(ext)
		switch contentType {
		case "":
			contentType = "application/octet-stream"
		case "image/gif":
			contentType = "video/mp4"
		default:

		}
		w.Header().Set("Content-Type", contentType)
		// 仅在不能预览时强制下载
		if !isPreviewable(contentType) {
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
		}
		w.Header().Set("Accept-Ranges", "bytes")
		io.Copy(w, resp.Body)
		return
	}

	// 否则为 fileAll.txt 模式（大文件组合下载）
	tgFile, err := bot.GetFile(tgbotapi.FileConfig{FileID: fileID})
	if err != nil {
		http.Error(w, "获取 fileAll.txt 失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", bot.Token, tgFile.FilePath)
	resp, err := http.Get(url)
	if err != nil {
		http.Error(w, "下载 fileAll.txt 失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("下载 fileAll.txt 返回状态异常: %d", resp.StatusCode), http.StatusInternalServerError)
		return
	}

	linesBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "读取 fileAll.txt 失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	linesStr := strings.Split(strings.TrimSpace(string(linesBytes)), "\n")

	// 去掉空行
	var cleanLines []string
	for _, line := range linesStr {
		line = strings.TrimSpace(line)
		if line != "" {
			cleanLines = append(cleanLines, line)
		}
	}

	if len(cleanLines) < 2 {
		http.Error(w, "fileAll.txt 格式错误，至少应有文件名和一个分块ID", http.StatusBadRequest)
		return
	}

	origFilename := cleanLines[0]
	blobFileIDs := cleanLines[1:]

	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", origFilename))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Accept-Ranges", "bytes")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "服务器不支持 Flush", http.StatusInternalServerError)
		return
	}

	log.Printf("开始下载合并大文件，文件名: %s，共 %d 个分块", origFilename, len(blobFileIDs))

	for idx, fid := range blobFileIDs {
		log.Printf("下载分块 %d/%d: %s", idx+1, len(blobFileIDs), fid)
		tgBlob, err := bot.GetFile(tgbotapi.FileConfig{FileID: fid})
		if err != nil {
			http.Error(w, fmt.Sprintf("获取分块 %s 失败: %v", fid, err), http.StatusInternalServerError)
			return
		}
		blobURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", bot.Token, tgBlob.FilePath)
		partResp, err := http.Get(blobURL)
		if err != nil {
			http.Error(w, fmt.Sprintf("下载分块 %s 失败: %v", fid, err), http.StatusInternalServerError)
			return
		}

		if partResp.StatusCode != http.StatusOK {
			partResp.Body.Close()
			http.Error(w, fmt.Sprintf("下载分块 %s 返回状态异常: %d", fid, partResp.StatusCode), http.StatusInternalServerError)
			return
		}

		// 复制数据时确认全部复制完毕
		nCopied, err := io.Copy(w, partResp.Body)
		partResp.Body.Close()
		if err != nil {
			http.Error(w, fmt.Sprintf("写入响应失败（分块 %s）: %v", fid, err), http.StatusInternalServerError)
			return
		}
		log.Printf("分块 %s 下载并写入完毕，字节数: %d", fid, nCopied)
		flusher.Flush()
	}

	log.Printf("大文件合并下载完成: %s", origFilename)
}

func handleVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "只支持 POST", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "解析表单失败", http.StatusBadRequest)
		return
	}
	if r.FormValue("pwd") == accessPwd {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	} else {
		http.Error(w, "密码错误", http.StatusUnauthorized)
	}
}

func getScheme(r *http.Request) string {
	// 优先使用反向代理头部判断协议
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		return proto
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func isPreviewable(contentType string) bool {
	return strings.HasPrefix(contentType, "image/") ||
		strings.HasPrefix(contentType, "video/") ||
		strings.HasPrefix(contentType, "audio/") ||
		contentType == "application/pdf"
}
