package main

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type cfsData struct {
	fs         []http.FileSystem
	dirs       []string
	pluginKeys []string
	Plugins    map[string]bool `json:"/plugins"`
}

type ChainedFileSystem struct {
	mutex sync.Mutex
	data  *cfsData
}

///////////////////////////////////////////////////////////////////////////////
//
///////////////////////////////////////////////////////////////////////////////
func (cfs *ChainedFileSystem) Open(name string) (http.File, error) {
	cfs.mutex.Lock()
	defer cfs.mutex.Unlock()

	data := cfs.data

	var lastIdx = len(data.fs) - 1

	for i := range data.fs {
		f, err := data.fs[i].Open(name)
		if i == lastIdx && err != nil {
			logger.Printf("Miss: %v\n", name)
			return nil, err
		} else if err == nil {
			logger.Printf("Hit: %v\n", name)
			return noReaddirFile{f}, nil
		}
	}

	return nil, errors.New("Algorithm failure")
}

///////////////////////////////////////////////////////////////////////////////
//
///////////////////////////////////////////////////////////////////////////////
func (cfs *ChainedFileSystem) checkNewPath(path string) {
	cfs.mutex.Lock()
	defer cfs.mutex.Unlock()

	data := cfs.data

	for _, existingPath := range data.dirs {
		if path == existingPath {
			break
		}
	}

	bundle_dir, _ := os.Open(path)
	subdirs, _ := bundle_dir.Readdirnames(-1)

	// There should only be one subdir with a unique name and a bundle html in it
	if len(subdirs) == 1 {
		_, err := os.Stat(filepath.Join(path, subdirs[0], "bundle.html"))

		if err == nil {
			pluginKey := subdirs[0] + "/bundle.html"
			_, exists := data.Plugins[pluginKey]
			if !exists {
				logger.Printf("ADDED BUNDLE %v\n", pluginKey)
				data.Plugins[pluginKey] = true
				data.pluginKeys = append(data.pluginKeys, pluginKey)
				data.dirs = append(data.dirs, path)
				data.fs = append(data.fs, http.Dir(path))
			}
		}
	}
}

///////////////////////////////////////////////////////////////////////////////
//
///////////////////////////////////////////////////////////////////////////////
func (cfs *ChainedFileSystem) cleanStalePaths() {
	cfs.mutex.Lock()
	defer cfs.mutex.Unlock()

	data := cfs.data

	newDirs := []string{}
	newFs := []http.FileSystem{}
	newKeys := []string{}

	for idx, _ := range data.dirs {
		_, err := os.Stat(data.dirs[idx])
		if err == nil {
			newDirs = append(newDirs, data.dirs[idx])
			newFs = append(newFs, data.fs[idx])
			newKeys = append(newKeys, data.pluginKeys[idx])
		} else {
			key := data.pluginKeys[idx]
			if key != "" {
				logger.Printf("REMOVED BUNDLE %v\n", key)
				delete(data.Plugins, key)
			}
		}
	}

	data.dirs = newDirs
	data.fs = newFs
	data.pluginKeys = newKeys
}

///////////////////////////////////////////////////////////////////////////////
//Initialize the ChainedFileSystem. dir is the bundle root
///////////////////////////////////////////////////////////////////////////////
func CFSInitialize(dir string) (*ChainedFileSystem, error) {
	file, _ := os.Open(dir)

	bundleNames, err := file.Readdirnames(-1)
	if err != nil {
		return nil, err
	}

	// Sort the bundle names to guarantee that file overrides/shadowing happen in that order
	sort.Strings(bundleNames)

	bundleFileSystems := make([]http.FileSystem, len(bundleNames), len(bundleNames))
	bundleDirs := make([]string, len(bundleNames), len(bundleNames))
	pluginKeys := make([]string, len(bundleNames), len(bundleNames))

	for idx, bundleName := range bundleNames {
		bundleDirs[idx] = filepath.Clean(bundle_root_dir + "/" + bundleName + "/web")
		bundleFileSystems[idx] = http.Dir(bundleDirs[idx])
		pluginKeys[idx] = ""
		logger.Printf("Bundle path %v added\n", bundle_root_dir+"/"+bundleName+"/web")
	}

	cfs := &ChainedFileSystem{data: &cfsData{fs: bundleFileSystems, dirs: bundleDirs, pluginKeys: pluginKeys, Plugins: map[string]bool{
		"plugins/authenticationPlugin.html":        true,
		"plugins/fileClientPlugin.html":            true,
		"plugins/jslintPlugin.html":                true,
		"javascript/plugins/javascriptPlugin.html": true,
		"edit/content/imageViewerPlugin.html":      true,
		"edit/content/jsonEditorPlugin.html":       true,
		"plugins/webEditingPlugin.html":            true,
		"plugins/pageLinksPlugin.html":             true,
		"plugins/preferencesPlugin.html":           true,
		"plugins/taskPlugin.html":                  true,
		"plugins/csslintPlugin.html":               true,
		"shell/plugins/shellPagePlugin.html":       true,
		"search/plugins/searchPagePlugin.html":     true,
		"golang/plugins/go-core.html":              true,
		"godev/go-godev.html":                      true,
	}}}

	// Poll the filesystem every so often to update the bundle caches
	go func() {
		for {
			for _, srcDir := range srcDirs {
				filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
					cfs.cleanStalePaths()
					if filepath.Base(path) == "godev-bundle" {
						cfs.checkNewPath(path)
					}

					return nil
				})
			}
			<-time.After(5 * time.Second)
		}
	}()

	return cfs, nil
}
