package singleton

import (
	"log"

	"github.com/BurntSushi/toml"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"

	"os"
	"path/filepath"

	"github.com/xos/serverstatus/model"
)

var Localizer *i18n.Localizer

func InitLocalizer() {
	bundle := i18n.NewBundle(language.Chinese)
	bundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)

	if _, exists := model.Languages[Conf.Language]; !exists {
		log.Println("NG>> language not exists:", Conf.Language)
		Conf.Language = "zh-CN"
	} else {
		langPath := filepath.Join("resource", "l10n", Conf.Language+".toml")
		_, err := bundle.LoadMessageFile(langPath)
		if err != nil && !os.IsNotExist(err) {
			panic(err)
		}
	}

	zhPath := filepath.Join("resource", "l10n", "zh-CN.toml")
	if _, err := bundle.LoadMessageFile(zhPath); err != nil {
		panic(err)
	}
	Localizer = i18n.NewLocalizer(bundle, Conf.Language)
}
