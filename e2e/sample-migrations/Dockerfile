# Sample migrations image for E2E testing
# This demonstrates the customer contract: image contains /migrations directory
# with SQL files and atlas.sum checksum file
#
# Note: atlas.sum is pre-generated using:
#   docker run --rm -v $(pwd)/migrations:/migrations arigaio/atlas migrate hash --dir file:///migrations
FROM scratch

# Copy migrations (including atlas.sum) into the image
COPY migrations /migrations
