// Send explicitly requested layout samples without starting or restarting the Bot.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type embed struct {
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Color       int               `json:"color"`
	Fields      []field           `json:"fields,omitempty"`
	Footer      map[string]string `json:"footer"`
}

func sample(variant string) embed {
	e := embed{Color: 0x527DA8, Footer: map[string]string{"text": "表示確認用サンプル · 曲の追加・再生は行いません"}}
	e.Description = "**再生中**\n**[藤井 風 - きらり (Official Video)](https://www.youtube.com/watch?v=TcLLpZBWsck)**\n3:52 · [Fujii Kaze](https://www.youtube.com/) · サンプルユーザー\n\n**次の再生予定**\n"
	titles := []string{"藤井 風 - 青春病 (Live at Nissan Stadium) [Day2] — 長いタイトルの折り返し確認", "藤井 風 - 花 (Official Video)", "YOASOBI「アイドル」 Official Music Video", "短い曲名"}
	ids := []string{"QPLviaYkuSs", "SfPkl7lol7g", "ZRtdQ81jPUQ", "TcLLpZBWsck"}
	numbers := []int{1, 2, 9, 10}
	combined := variant == "df11"
	pageTwo := variant == "d11" || combined
	if pageTwo {
		originalTitles, originalIDs := titles, ids
		titles, ids, numbers = nil, nil, nil
		for i := 0; i < 10; i++ {
			titles = append(titles, originalTitles[i%len(originalTitles)])
			ids = append(ids, originalIDs[i%len(originalIDs)])
			numbers = append(numbers, 11+i)
		}
		variant = "d"
	}
	switch variant {
	case "a":
		e.Title = "A：通常テキストの番号・補足は左揃え"
	case "b":
		e.Title = "B：通常テキストの番号・補足を字下げ"
	case "c":
		e.Title = "C：曲ごとにEmbedフィールド"
	case "d":
		e.Title = "D：自動番号・行末スペース2個で改行"
	case "e":
		e.Title = "E：自動番号・行末バックスラッシュで改行"
	case "f":
		e.Title = "F：自動番号・補足行にsubtext記法"
	}
	for i, title := range titles {
		link := "**[" + title + "](https://www.youtube.com/watch?v=" + ids[i] + ")**"
		detail := "3:52 · [チャンネル名](https://www.youtube.com/) · サンプルユーザー"
		if variant == "c" {
			e.Fields = append(e.Fields, field{Name: fmt.Sprintf("%d. %s", numbers[i], title), Value: "[動画を開く](https://www.youtube.com/watch?v=" + ids[i] + ")\n" + detail})
			continue
		}
		indent := ""
		if variant == "d" || variant == "e" || variant == "f" {
			prefix := fmt.Sprintf("%d. ", numbers[i])
			indent = strings.Repeat(" ", len(prefix))
			lineEnd := "  "
			if combined {
				detail = "-# " + detail
			}
			if variant == "e" {
				lineEnd = "\\"
			}
			if variant == "f" {
				lineEnd = ""
				detail = "-# " + detail
			}
			e.Description += prefix + link + lineEnd + "\n" + indent + detail + "\n"
			continue
		}
		if variant == "b" {
			indent = "　"
			if numbers[i] >= 10 {
				indent = "　 "
			}
		}
		e.Description += fmt.Sprintf("%d\\. %s\n%s%s\n", numbers[i], link, indent, detail)
	}
	if variant != "c" {
		e.Description += "\n**自動再生 ON**\n待機キュー終了後に自動再生します。"
	}
	if pageTwo {
		e.Title = "D方式：11〜20番のリスト"
		if combined {
			e.Title = "D＋F：強制改行とsubtext・11〜20番"
		}
		e.Footer["text"] = "2 / 2ページの表示検証 · サンプル音源を繰り返して配置"
	}
	return e
}

func run() error {
	variants := os.Args[1:]
	if len(variants) == 0 {
		variants = []string{"a", "b", "c"}
	}
	for _, v := range variants {
		if v != "d11" && v != "df11" && (len(v) != 1 || !strings.Contains("abcdef", v)) {
			return fmt.Errorf("choose a through f, d11, or df11")
		}
	}
	env, err := godotenv.Read(".env.ui-preview")
	if err != nil {
		return fmt.Errorf("cannot read .env.ui-preview")
	}
	u, err := url.Parse(env["DISCORD_UI_PREVIEW_WEBHOOK"])
	if err != nil || u.Scheme != "https" || u.Host != "discord.com" || !strings.HasPrefix(u.Path, "/api/webhooks/") || u.User != nil {
		return fmt.Errorf("invalid Discord preview webhook")
	}
	q := u.Query()
	q.Set("wait", "true")
	u.RawQuery = q.Encode()
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, v := range variants {
		preview := sample(v)
		if userID := os.Getenv("UI_PREVIEW_USER_ID"); userID != "" {
			if len(userID) < 17 || len(userID) > 20 || strings.Trim(userID, "0123456789") != "" {
				return fmt.Errorf("invalid UI_PREVIEW_USER_ID")
			}
			preview.Description = strings.ReplaceAll(preview.Description, "サンプルユーザー", "<@"+userID+">")
		}
		payload := map[string]any{"username": "AsterinMusic UI検証", "content": "レイアウト比較（検証用サンプル）", "embeds": []embed{preview}, "allowed_mentions": map[string]any{"parse": []string{}}}
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("cannot encode preview")
		}
		req, err := http.NewRequest(http.MethodPost, u.String(), bytes.NewReader(b))
		if err != nil {
			return fmt.Errorf("cannot create request")
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("preview request failed (details suppressed)")
		}
		var result struct {
			ID string `json:"id"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("preview %s failed: HTTP %d; no automatic retry", v, resp.StatusCode)
		}
		if decodeErr != nil || result.ID == "" {
			return fmt.Errorf("preview %s response unreadable; message may have been sent", v)
		}
		fmt.Printf("preview %s sent: message %s\n", v, result.ID)
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
