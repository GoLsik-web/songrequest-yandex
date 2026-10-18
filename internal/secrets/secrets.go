// Package secrets хранит токены в системном хранилище учётных данных
// (на Windows — «Диспетчер учётных данных»), а не в файле рядом с настройками.
//
// Запасного варианта с открытым файлом сознательно нет: тихо положить токен
// Spotify в текстовый файл на чужом компьютере — хуже, чем честно сказать,
// что хранилище недоступно.
package secrets

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

const service = "songrequest"

// ErrNotFound означает, что вход ещё не выполнялся.
var ErrNotFound = errors.New("сохранённых данных нет")

// Store — доступ к системному хранилищу.
type Store struct{}

// New создаёт хранилище.
func New() *Store { return &Store{} }

// PutJSON кладёт структуру под именем name.
func (s *Store) PutJSON(name string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := keyring.Set(service, name, string(data)); err != nil {
		return fmt.Errorf("не смог сохранить данные входа в хранилище Windows: %w", err)
	}
	return nil
}

// GetJSON читает структуру; ErrNotFound, если её там нет.
func (s *Store) GetJSON(name string, v any) error {
	data, err := keyring.Get(service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("не смог прочитать данные входа из хранилища Windows: %w", err)
	}
	return json.Unmarshal([]byte(data), v)
}

// Delete забывает сохранённое: нужно для кнопки «Отключить».
func (s *Store) Delete(name string) error {
	err := keyring.Delete(service, name)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}
