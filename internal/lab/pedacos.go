package lab

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// baixarPorPedacos usa os resumos por pedaço do metalink oficial. Cada
// rodada lê o arquivo inteiro, marca os pedaços ruins ou ausentes e baixa
// só esses trechos. Serve para retomar downloads de outro programa (como o
// aria2) e para disco que corrompe dados sem dar erro: em vez de rejeitar
// o arquivo inteiro, refaz só o que estragou.
func (a *Atualizador) baixarPorPedacos(ctx context.Context, url, parcial, nome string, total int64, resumo string, tam int64, hashes []string) error {
	f, err := os.OpenFile(parcial, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if st, _ := f.Stat(); st.Size() != total {
		// Tamanho final de uma vez: o que falta fica como pedaço ruim.
		if err := f.Truncate(total); err != nil {
			return err
		}
	}
	for rodada := 1; rodada <= 5; rodada++ {
		_ = f.Sync()
		esquecerCache(parcial)
		ruins, soma, err := a.conferirPedacos(ctx, f, nome, total, tam, hashes)
		if err != nil {
			return err
		}
		if len(ruins) == 0 {
			if soma != resumo {
				return errors.New("pedaços conferem mas o SHA-256 final não; verificar o disco")
			}
			return nil
		}
		log.Printf("atualização: %s: rodada %d, %d de %d pedaços a baixar", nome, rodada, len(ruins), len(hashes))
		if err := a.baixarTrechos(ctx, f, url, nome, total, tam, hashes, ruins); err != nil {
			return err
		}
	}
	return errors.New("pedaços continuam corrompidos depois de 5 rodadas; verificar o disco")
}

// conferirPedacos lê o arquivo em ordem, devolve os pedaços que não batem
// e o SHA-256 do arquivo inteiro, calculado na mesma leitura.
func (a *Atualizador) conferirPedacos(ctx context.Context, f *os.File, nome string, total, tam int64, hashes []string) ([]int, string, error) {
	todo := sha256.New()
	buf := make([]byte, tam)
	var ruins []int
	ultimo := time.Now()
	for i := range hashes {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		n := min(tam, total-int64(i)*tam)
		if _, err := f.ReadAt(buf[:n], int64(i)*tam); err != nil && !errors.Is(err, io.EOF) {
			// Erro de leitura no disco: o pedaço é refeito.
			ruins = append(ruins, i)
			todo.Write(make([]byte, n))
			continue
		}
		todo.Write(buf[:n])
		h := sha1.Sum(buf[:n])
		if !strings.EqualFold(hex.EncodeToString(h[:]), strings.TrimSpace(hashes[i])) {
			ruins = append(ruins, i)
		}
		if time.Since(ultimo) > 2*time.Second {
			pct := (i + 1) * 100 / len(hashes)
			a.mu.Lock()
			a.baixando[nome] = &andamento{texto: fmt.Sprintf("Conferindo pedaços: %d de %d, %d a refazer.", i+1, len(hashes), len(ruins)), pct: &pct}
			a.mu.Unlock()
			ultimo = time.Now()
		}
	}
	return ruins, hex.EncodeToString(todo.Sum(nil)), nil
}

// baixarTrechos junta pedaços ruins seguidos num só pedido HTTP com faixa
// de bytes e confere cada pedaço antes de gravar.
func (a *Atualizador) baixarTrechos(ctx context.Context, f *os.File, url, nome string, total, tam int64, hashes []string, ruins []int) error {
	feitos := 0
	ultimo := time.Now()
	for k := 0; k < len(ruins); {
		ini := ruins[k]
		fim := ini
		for k+1 < len(ruins) && ruins[k+1] == fim+1 {
			k++
			fim++
		}
		k++
		for tentativa := 0; ini <= fim; tentativa++ {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if tentativa >= 10 {
				return fmt.Errorf("pedaço %d não baixou depois de 10 tentativas", ini)
			}
			de, ate := int64(ini)*tam, min(int64(fim+1)*tam, total)-1
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			req.Header.Set("User-Agent", "offline-library/"+Versao)
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", de, ate))
			resp, err := a.http.Do(req)
			if err != nil {
				time.Sleep(30 * time.Second)
				continue
			}
			if resp.StatusCode != http.StatusPartialContent {
				resp.Body.Close()
				return fmt.Errorf("servidor não aceitou faixa de bytes (%s)", resp.Status)
			}
			buf := make([]byte, tam)
			for ini <= fim {
				n := min(tam, total-int64(ini)*tam)
				if _, err := io.ReadFull(resp.Body, buf[:n]); err != nil {
					break // conexão caiu: nova tentativa a partir deste pedaço
				}
				h := sha1.Sum(buf[:n])
				if !strings.EqualFold(hex.EncodeToString(h[:]), strings.TrimSpace(hashes[ini])) {
					break // veio errado da rede: pede de novo
				}
				if _, err := f.WriteAt(buf[:n], int64(ini)*tam); err != nil {
					resp.Body.Close()
					return err
				}
				ini++
				feitos++
				tentativa = 0
				if time.Since(ultimo) > 2*time.Second {
					pct := feitos * 100 / len(ruins)
					a.mu.Lock()
					a.baixando[nome] = &andamento{texto: fmt.Sprintf("Baixando pedaços: %d de %d.", feitos, len(ruins)), pct: &pct}
					a.mu.Unlock()
					ultimo = time.Now()
				}
			}
			resp.Body.Close()
		}
	}
	return f.Sync()
}
