# The installed command's name and place; override either on the command
# line, e.g. make install NAME=sessions.
NAME   ?= ,sessions
BINDIR ?= $(HOME)/.local/bin

.PHONY: build install

# Both targets are phony: go build keeps its own cache and knows better
# than make what is stale. Building the package (.) rather than main.go
# stamps the commit into the binary, so go version -m names the one you
# have installed. The binary goes to bin/: at the root, NAME=sessions
# would name the package directory, and go build -o writes into one.
build:
	go build -o bin/$(NAME) .

# install unlinks the old file before writing the new one, so a picker
# still running from it carries on instead of failing on "text file busy".
install: build
	install -Dm755 bin/$(NAME) $(BINDIR)/$(NAME)
