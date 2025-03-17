#!/bin/bash
# Script to clean up untagged package versions from GitHub Container Registry
# For user: bnema, package: gordon

# Exit on error
set -e

PACKAGE_NAME="gordon"
ENV_PATH="./.env"

# Colors for output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo -e "${YELLOW}GitHub Container Registry Cleanup Script${NC}"
echo -e "${YELLOW}======================================${NC}"

# Check if jq is installed
if ! command -v jq &> /dev/null; then
    echo -e "${RED}Error: jq is not installed.${NC}"
    echo "Please install it using your package manager (e.g., apt install jq, brew install jq)"
    exit 1
fi

# Load environment variables from .env file if it exists
if [ -f "$ENV_PATH" ]; then
    echo -e "${GREEN}Loading credentials from .env file...${NC}"
    export $(grep -v '^#' "$ENV_PATH" | xargs)
fi

# Check for credentials
if [ -z "$GHCR_USERNAME" ] || [ -z "$GHCR_TOKEN" ]; then
    # Prompt for credentials if not set as environment variables
    echo -e "${RED}GitHub Container Registry credentials not found in environment.${NC}"
    echo -e "${YELLOW}Please set GHCR_USERNAME and GHCR_TOKEN environment variables:${NC}"
    echo -e "${GREEN}export GHCR_USERNAME=your_username${NC}"
    echo -e "${GREEN}export GHCR_TOKEN=your_token${NC}"
    exit 1
fi

echo -e "${GREEN}Fetching untagged package versions for ${GHCR_USERNAME}/${PACKAGE_NAME}...${NC}"
echo -e "${YELLOW}Authenticating and fetching versions...${NC}"

# Initialize variables for pagination
PAGE=1
PER_PAGE=100
UNTAGGED_VERSIONS="[]"
TOTAL_COUNT=0

# Fetch all pages
while true; do
    echo -e "${YELLOW}Fetching page ${PAGE}...${NC}"
    
    # Get versions with pagination
    VERSIONS_RESPONSE=$(curl -s -H "Authorization: Bearer ${GHCR_TOKEN}" \
        "https://api.github.com/users/${GHCR_USERNAME}/packages/container/${PACKAGE_NAME}/versions?per_page=${PER_PAGE}&page=${PAGE}")

    # Check for errors in the response
    if echo "$VERSIONS_RESPONSE" | grep -q "message"; then
        echo -e "${RED}Error fetching package versions:${NC}"
        echo "$VERSIONS_RESPONSE" | jq -r '.message'
        echo -e "${YELLOW}Make sure your token has the 'read:packages' and 'delete:packages' scopes.${NC}"
        exit 1
    fi

    # Get untagged versions from this page
    PAGE_UNTAGGED=$(echo "$VERSIONS_RESPONSE" | jq '[.[] | select(.metadata.container.tags | length == 0) | {id: .id, created_at: .created_at}]')
    PAGE_COUNT=$(echo "$PAGE_UNTAGGED" | jq 'length')
    
    # Add to our collection
    UNTAGGED_VERSIONS=$(echo "$UNTAGGED_VERSIONS" "$PAGE_UNTAGGED" | jq -s 'add')
    TOTAL_COUNT=$((TOTAL_COUNT + PAGE_COUNT))
    
    # Check if we got fewer results than requested, which means we're on the last page
    RESULTS_COUNT=$(echo "$VERSIONS_RESPONSE" | jq 'length')
    if [ "$RESULTS_COUNT" -lt "$PER_PAGE" ]; then
        break
    fi
    
    # Move to next page
    PAGE=$((PAGE + 1))
done

# Count untagged versions
COUNT=$(echo "$UNTAGGED_VERSIONS" | jq 'length')

if [ "$COUNT" -eq 0 ]; then
    echo -e "${GREEN}No untagged versions found.${NC}"
    exit 0
fi

echo -e "${YELLOW}Found ${COUNT} untagged version(s) out of ${TOTAL_COUNT} total:${NC}"
echo "$UNTAGGED_VERSIONS" | jq -r '.[] | "\(.id) (created: \(.created_at))"'

echo
read -p "Do you want to delete these untagged versions? (y/n): " CONFIRM

if [[ "$CONFIRM" != "y" && "$CONFIRM" != "Y" ]]; then
    echo -e "${YELLOW}Operation cancelled.${NC}"
    exit 0
fi

echo -e "${YELLOW}Deleting untagged versions...${NC}"

# Delete untagged versions
DELETED_COUNT=0
for ID in $(echo "$UNTAGGED_VERSIONS" | jq -r '.[].id'); do
    echo -e "Deleting version ID: ${ID}"
    DELETE_RESPONSE=$(curl -s -X DELETE -H "Authorization: Bearer ${GHCR_TOKEN}" "https://api.github.com/users/${GHCR_USERNAME}/packages/container/${PACKAGE_NAME}/versions/${ID}")
    
    if [ -z "$DELETE_RESPONSE" ]; then
        echo -e "${GREEN}Successfully deleted version ID: ${ID}${NC}"
        DELETED_COUNT=$((DELETED_COUNT + 1))
    else
        echo -e "${RED}Failed to delete version ID: ${ID}${NC}"
        echo "$DELETE_RESPONSE" | jq -r '.message // .'
    fi
done

echo -e "${GREEN}Cleanup completed! Deleted ${DELETED_COUNT} untagged versions.${NC}"