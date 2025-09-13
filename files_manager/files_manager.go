package filesmanager

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/alonsovidales/otc/cfg"
	"github.com/alonsovidales/otc/dao"
	"github.com/alonsovidales/otc/log"
	pb "github.com/alonsovidales/otc/proto/generated"
	"github.com/alonsovidales/otc/session"
	"golang.org/x/image/draw"
	"google.golang.org/protobuf/types/known/timestamppb"
	"image"
	"image/jpeg"
	_ "image/jpeg"
	"math"
	"net/http"
	"os"
	"runtime"
)

const (
	// CRegenerateGroupKey Endpoint used to regenerate the security key for
	// a shard
	CGet = "/get"
)

// Manager Structure that provides HTTP access to manage all the different
// groups and shards on each grorup
type Manager struct {
	baseUrl    string
	dao        *dao.Dao
	maxUploads chan bool
	encoders   *Encoders
}

func Init(baseUrl string, dao *dao.Dao) *Manager {
	mg := &Manager{
		baseUrl:    baseUrl,
		dao:        dao,
		maxUploads: make(chan bool, runtime.NumCPU()-1), // Leave one CPU free for other stuff
	}

	var err error
	mg.encoders, err = NewEncoders(
		"models/clip_image.onnx",
		"models/clip_text.onnx",
		"models/vocab.json",
		"models/merges.txt",
		224, // input size
		77,  // max seq len
		512, // embedding dim
	)

	if err != nil {
		log.Fatal("Error loading image encoders:", err)
	}

	return mg
}

// RegenerateGroupKey Creates a new random key for a group
func (mg *Manager) Get(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	file := r.FormValue("file")

	w.WriteHeader(200)
	w.Write([]byte(fmt.Sprintf("Good : %s", file)))
}

func (mg *Manager) ListFiles(session *session.Session, path string) (files []*pb.File, err error) {
	return mg.dao.GetFilesByPath(path, false, true)
}

func (mg *Manager) cosineSimilarity(a, b []float32) float32 {
	var dot, na, nb float32
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	return dot / (float32(math.Sqrt(float64(na))) * float32(math.Sqrt(float64(nb))))
}

func (mg *Manager) ImageSearch(session *session.Session, path, text string) (files []*pb.File, err error) {
	files, err = mg.dao.GetFilesByPath(path, true, false)
	if err != nil {
		return nil, err
	}
	tensor, err := mg.encoders.RunText(text)
	if err != nil {
		return nil, err
	}

	simlarities := make([]float32, len(files))
	for i, file := range files {
		simlarities[i] = mg.cosineSimilarity(tensor, file.Embedding)
		log.Debug("Similarity:", simlarities[i])

		// Populate the thumbnails
		encContent, err := os.ReadFile(fmt.Sprintf("%s/%s_thumbnail", cfg.GetStr("otc", "storage-path"), files[i].Hash))
		if err != nil {
			encContent, err = os.ReadFile(fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), files[i].Hash))
			if err != nil {
				return nil, err
			}
		}
		files[i].Content, err = session.Decrypt(encContent)
		if err != nil {
			return nil, err
		}
	}

	maxSim := float32(math.Inf(-1))
	maxSimPos := 0
	filesToReturn := make([]*pb.File, len(files))
	// Short the documents by similarity in descending order
	for i := range len(files) {
		for simPos, sim := range simlarities {
			if sim > maxSim {
				maxSimPos = simPos
				maxSim = sim
			}
		}

		log.Debug("Max Sim:", maxSim, maxSimPos)
		filesToReturn[i] = files[maxSimPos]
		simlarities[maxSimPos] = float32(math.Inf(-1))
		maxSim = float32(math.Inf(-1))
	}

	return filesToReturn, nil
}

func (mg *Manager) GetFile(session *session.Session, path string) (file *pb.File, err error) {
	file, err = mg.dao.GetFileByPath(path)
	if err == nil {
		encContent, err := os.ReadFile(fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), file.Hash))
		if err != nil {
			log.Error("error reading file from:", path, err)
		}
		file.Content, err = session.Decrypt(encContent)
		if err != nil {
			log.Error("error decryptinig the datra", err)
		}
	}

	return
}

func (mg *Manager) DelFile(session *session.Session, path string) (err error) {
	file, err := mg.dao.GetFileByPath(path)
	if err != nil {
		return
	}
	err = mg.dao.DelFileByPath(path)
	if err != nil {
		return
	}

	file, _ = mg.dao.GetFileByPath(path)
	if file != nil {
		// We don't delete the file since we still have another
		// reference with another path
		return nil
	}

	fullPath := fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), file.Hash)
	if err = os.Remove(fullPath); err != nil {
		return err
	}
	os.Remove(fmt.Sprintf("%s_thumbnail", fullPath))
	return
}

func (mg *Manager) UploadFile(session *session.Session, path string, content []byte, forceOverride bool) (file *pb.File, err error) {
	mimeType := http.DetectContentType(content)
	log.Debug("Mime type:", mimeType)

	// Calculate the SHA256 of the file to be used as unique hash
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	log.Debug("Calculated Hash:", hash)

	file = &pb.File{
		Created:  timestamppb.Now(),
		Modified: timestamppb.Now(),
		Path:     path,
		Mime:     mimeType,
		Hash:     hash,
		Size:     int32(len(content)),
	}

	duplicated, err := mg.dao.StoreNewFile(file)
	if err != nil {
		return
	}

	if duplicated {
		file, err = mg.dao.GetFileByPath(path)
		if err != nil {
			return nil, err
		}
		if file.Hash == hash {
			log.Debug("Same file with same content for:", path, hash)
			return file, nil
		}
		if forceOverride {
			mg.DelFile(session, path)
			_, err = mg.dao.StoreNewFile(file)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, errors.New("Duplicated file")
		}
	}

	targetPath := fmt.Sprintf("%s/%s", cfg.GetStr("otc", "storage-path"), hash)

	// Limit the amounth of concurrent writes
	mg.maxUploads <- true

	go func(targetPath string) {
		defer func() { <-mg.maxUploads }()

		// Write to disk the content
		err = os.WriteFile(targetPath, session.Encrypt(content), 0644) // perms: rw-r--r--

		// We will try to create a thumbnail of images only
		if mimeType[:5] == "image" {
			file.Embedding, err = mg.encoders.RunImage(content) // ML model to classify the image
			if err != nil {
				log.Error("error analyzing the image:", err)
				return
			}
			err = mg.dao.UpdateImageEmbedding(file)
			if err != nil {
				log.Error("error updating image embeddings:", err)
				return
			}

			img, _, err := image.Decode(bytes.NewReader(content))
			if err != nil {
				log.Error("error decoding the image:", err)
				return
			}
			imgCfg, _, err := image.DecodeConfig(bytes.NewReader(content))
			if err != nil {
				log.Error("error decoding image config:", err)
				return
			}
			maxWidth := int(cfg.GetInt("otc", "max-thumbnail-width-px"))
			if imgCfg.Width > maxWidth {
				newH := int(float64(imgCfg.Height) * float64(maxWidth) / float64(imgCfg.Width))
				dst := image.NewRGBA(image.Rect(0, 0, maxWidth, newH))
				draw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Over, nil)
				var buf bytes.Buffer
				jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80})
				log.Debug("Thumbnail:", fmt.Sprintf("%s_thumbnail", targetPath))
				err = os.WriteFile(fmt.Sprintf("%s_thumbnail", targetPath), session.Encrypt(buf.Bytes()), 0644)
				if err != nil {
					log.Error("Error generating thumbnail:", err)
				}
			}
		}
	}(targetPath)

	return
}
