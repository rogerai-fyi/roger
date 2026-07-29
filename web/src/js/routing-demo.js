(() => {
  "use strict";

  const root = document.getElementById("routeSim");
  const next = document.getElementById("routeNext");
  if (!root || !next) return;

  const stations = [...root.querySelectorAll(".route-station")];
  const lines = [...root.querySelectorAll(".route-line")];
  const request = document.getElementById("routeRequest");
  const choice = document.getElementById("routeChoice");
  const why = document.getElementById("routeWhy");

  // Illustrative sequence: same ten stations, capacity-aware load changes.
  // This is intentionally deterministic and labeled SIMULATION in the UI.
  const steps = [
    { pick: 0, loads: [1,0,0,0,0,0,0,0,0,0], why: "A1 has the strongest healthy score and open capacity." },
    { pick: 1, loads: [1,1,0,0,0,0,0,0,0,0], why: "B7 wins the next comparison: almost the same merit, lower normalized load." },
    { pick: 0, loads: [2,1,0,0,0,0,0,0,0,0], why: "A1 takes a second request and reaches its measured capacity." },
    { pick: 3, loads: [2,1,0,1,0,0,0,0,0,0], why: "A1 is full. D9 is healthy, compatible, and available now." },
    { pick: 1, loads: [2,2,0,1,0,0,0,0,0,0], why: "B7 still has room: two inflight requests across four measured slots." },
    { pick: 2, loads: [2,2,1,1,0,0,0,0,0,0], why: "C3 earns traffic from the reliable top band and fills its single slot." },
    { pick: 7, loads: [1,2,1,1,0,0,0,1,0,0], why: "A1 completes one; H8 is explored after passing its canary and showing capacity." },
    { pick: 0, loads: [2,2,1,1,0,0,0,1,0,0], why: "A1 is available again. The route returns without sticky assignment." }
  ];
  const capacities = [2,4,1,2,1,2,1,4,2,1];
  let index = 0;

  function render() {
    const step = steps[index];
    root.dataset.step = String(index);
    request.textContent = String(index + 1).padStart(2, "0");
    choice.textContent = stations[step.pick].querySelector("b").textContent;
    why.textContent = step.why;

    stations.forEach((station, i) => {
      const load = step.loads[i];
      station.classList.toggle("is-picked", i === step.pick);
      station.classList.toggle("is-busy", load >= capacities[i]);
      station.querySelector("small").textContent = `${load} / ${capacities[i]}`;
    });
    lines.forEach((line, i) => line.classList.toggle("is-route", i === step.pick));
  }

  next.addEventListener("click", () => {
    index = (index + 1) % steps.length;
    render();
  });

  render();
})();
