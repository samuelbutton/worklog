BIN ?= $(HOME)/.local/bin
PLIST := $(HOME)/Library/LaunchAgents/local.worklog.watch.plist

.PHONY: build test install watch-at-login no-watch-at-login
build:
	go build -o worklog .
test:
	go vet ./... && go test ./...
install: test
	mkdir -p $(BIN) && go build -o $(BIN)/worklog .

# Runs `worklog watch` in the background from login onward; output goes to ~/worklog/watch.log.
watch-at-login: install
	mkdir -p $(HOME)/Library/LaunchAgents $(HOME)/worklog
	sed -e 's|@BIN@|$(BIN)|' -e 's|@HOME@|$(HOME)|g' watch.plist.in > $(PLIST)
	-launchctl bootout gui/$$(id -u) $(PLIST) 2>/dev/null
	launchctl bootstrap gui/$$(id -u) $(PLIST)

no-watch-at-login:
	-launchctl bootout gui/$$(id -u) $(PLIST)
	rm -f $(PLIST)
