# Plots miaoclock history (hourly min/max temperature and humidity).
# Normally run through plot.sh. Directly:
#   gnuplot -c plot.gp <csv with timezone offsets removed> <out.png>
csv = ARG1
out = ARG2

set terminal pngcairo size 1400,800 font "Helvetica,11"
set output out
set datafile separator ","
set xdata time
set timefmt "%Y-%m-%dT%H:%M:%S"
set format x "%b %d"
set key outside top center horizontal
set grid

set multiplot layout 2,1
set ylabel "Temperature (°C)"
plot csv skip 1 using 2:3 with lines lc rgb "#d1495b" title "max", \
     csv skip 1 using 2:5 with lines lc rgb "#2e86ab" title "min"
set ylabel "Humidity (%)"
plot csv skip 1 using 2:4 with lines lc rgb "#d1495b" title "max", \
     csv skip 1 using 2:6 with lines lc rgb "#2e86ab" title "min"
unset multiplot
