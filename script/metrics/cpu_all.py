import pandas as pd
import matplotlib.pyplot as plt
import sys

file_path = sys.argv[1]

df = pd.read_csv(file_path)

# Parse the "time" column if it contains datetime values (example)
# Assuming the timestamps are in the "time" column of the CSV
if "time" in df.columns:
    df["time"] = pd.to_datetime(df["time"], errors="coerce")

# Extract only rows where the "name" column is "cpu_all"
df_cpu_all = df[df["name"] == "cpu_all"].copy()

# Extract rows with specific label values (user, system, etc.)
# Filter using an explicit list of categories
valid_labels = [
    "user",
    "system",
    "idle",
    "iowait",
    "irq",
    "nice",
    "softirq",
    "steal",
]
df_cpu_all = df_cpu_all[df_cpu_all["label"].isin(valid_labels)]

# Assuming the "value" column contains CPU usage (%)
# Pivot the data with time as index and label as columns
# (rows: time, columns: label, values: value)
if "time" in df_cpu_all.columns:
    df_cpu_all.set_index("time", inplace=True)

# Create pivot table with CPU usage data
if df_cpu_all.empty:
    print("Error: Input DataFrame is empty")
    sys.exit(1)

df_pivot = df_cpu_all.pivot_table(
    index=df_cpu_all.index, columns="label", values="value", aggfunc="mean"
).ffill()

# Fill remaining NaN values with 0 (if any NaN exists at the start of data)
df_pivot = df_pivot.fillna(0)

# Create a stacked area graph
# Sort the data by valid_labels for better visualization
df_pivot = df_pivot[valid_labels]  # Sort by specified label order
ax = df_pivot.plot(kind="area", stacked=True, figsize=(10, 6))

# Set axis labels and title
ax.set_xlabel("Time")
ax.set_ylabel("CPU Usage (%)")
ax.set_title("CPU Usage Stacked Area (cpu_all)")
ax.legend(loc="upper left")

# Display the graph
plt.tight_layout()
plt.show()
