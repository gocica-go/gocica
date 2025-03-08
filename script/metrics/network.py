import pandas as pd
import matplotlib.pyplot as plt
import sys

file_path = sys.argv[1]

df = pd.read_csv(file_path)

# Parse time column if it contains datetime values
df["time"] = pd.to_datetime(df["time"], errors="coerce")

# Extract only "network_rx" and "network_tx" rows
df_network = df[df["name"].isin(["network_rx", "network_tx"])].copy()

# Pivot cumulative bytes received (network_rx)
df_rx = (
    df_network[df_network["name"] == "network_rx"]
    .pivot_table(index="time", columns="label", values="value", aggfunc="last")
    .ffill()
)

# Pivot cumulative bytes sent (network_tx)
df_tx = (
    df_network[df_network["name"] == "network_tx"]
    .pivot_table(index="time", columns="label", values="value", aggfunc="last")
    .ffill()
)
print(df_tx)

# Sort by time
df_rx = df_rx.sort_index()
df_tx = df_tx.sort_index()

# Calculate difference from previous values to get byte increment per interval
df_rx_diff = df_rx.diff(periods=10)
df_tx_diff = df_tx.diff(periods=10)

# Calculate time difference in seconds
time_rx_diff = df_rx.index.to_series().diff(periods=10).dt.total_seconds()
time_tx_diff = df_tx.index.to_series().diff(periods=10).dt.total_seconds()

# Calculate bytes per second (bytes/s)
# Use axis=0 for column-wise broadcast (each interface has its own column)
df_rx_rate = df_rx_diff.div(time_rx_diff, axis=0)
df_tx_rate = df_tx_diff.div(time_tx_diff, axis=0)

df_rx_rate = df_rx_rate / 1e6  # Convert to MB/s
df_tx_rate = df_tx_rate / 1e6  # Convert to MB/s

# Remove first row which is NaN due to diff operation
df_rx_rate.dropna(inplace=True)
df_tx_rate.dropna(inplace=True)

print(df_tx_rate)

# Plot graphs
fig, (ax1, ax2) = plt.subplots(2, 1, figsize=(12, 8), sharex=True)

# Plot RX
df_rx_rate.plot(ax=ax1)
ax1.set_title("Network RX (MB/s)")
ax1.set_ylabel("MB/s")

# Plot TX
df_tx_rate.plot(ax=ax2)
ax2.set_title("Network TX (MB/s)")
ax2.set_ylabel("MB/s")

plt.tight_layout()
plt.show()
